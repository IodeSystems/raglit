// Package raglit is a local, composable document RAG index.
//
// The whole index is ONE portable SQLite file. SQLite's FTS5 extension gives
// BM25 lexical ranking built in, so "BM25" and "the document:page:fragment
// index" collapse into a single dependency — modernc.org/sqlite, which is
// pure-Go (no CGo) and thus builds to a single static binary. That is the point
// of raglit: a tool small enough to drop into any workflow (index a folder,
// grep it semantically) that scales up to a real service by swapping the
// agent.DocFinder impl (see finder.go) for a remote one — no rewrite.
//
// Grain: documents → fragments(page, ord, text). A "fragment" is one indexable
// unit (a paragraph, a chunk, an OCR'd page region); page + ord locate it back
// in the source so a hit is a precise citation, not just "somewhere in the PDF".
//
// Vectors are deliberately absent in v1. FTS5 lexical BM25 is the floor; a
// vector sidecar (sqlite-vec, or a custom NSW file) is added only if lexical
// recall proves insufficient — measured, not assumed.
package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "embed"

	gen "github.com/iodesystems/raglit/internal/db"

	_ "modernc.org/sqlite"
)

// Store is a handle to one raglit index file. When opened via OpenHome it also
// knows a Home layout and copies ingested originals into it.
type Store struct {
	db       *sql.DB
	q        *gen.Queries // sqlc/metaquery typed CRUD over db (FTS/vec stay raw)
	path     string
	home     Home
	withHome bool
	embedder *Embedder // nil → lexical only; set for vector/hybrid search
	// imageEmbedder, when set, embeds figure IMAGES (a CLIP-style tower) for
	// figure search; nil → figures fall back to embedding their DESCRIPTION with
	// the text embedder (same space as fragments, so text queries can match).
	imageEmbedder ImageEmbedder
	// identifier, when set, asks a model what each ingested document IS — a
	// caption, a summary and a kind (identity.go). nil → documents keep only the
	// filename they arrived with.
	identifier *Identifier
	// extractEmailAttachments writes a mail archive's attachments into
	// <archive>.raglit-attachments/ beside it. Off for the same reason, and with
	// more force — one archive can carry 69 files.
	extractEmailAttachments bool
	// parent, when set, makes this Store a BRANCH: reads overlay branch-over-
	// parent at document grain (a branch doc / tombstone shadows the parent's).
	// Writes go to the branch only (copy-on-write). See branch.go.
	parent *Store
}

// SetParent makes this store a branch over p (branch-over-parent overlay reads).
func (s *Store) SetParent(p *Store) { s.parent = p }

// gq returns a generated Queries bound to a transaction (for atomic writes).
func gq(tx gen.DBTX) *gen.Queries { return gen.New(tx) }

// Path is the index file path (or ":memory:").
func (s *Store) Path() string { return s.path }

// DocumentHash returns a document's stored source-content hash, or "" if the doc
// is unknown or has none. The worker uses it to skip re-ingesting unchanged
// content (dedup work).
func (s *Store) DocumentHash(path string) (string, error) {
	h, err := s.q.GetDocumentHash(context.Background(), path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

// SetDocumentHash records a document's source-content hash after a successful
// ingest, so an unchanged re-ingest can be skipped.
func (s *Store) SetDocumentHash(path, hash string) error {
	return s.q.SetDocumentHash(context.Background(), gen.SetDocumentHashParams{ContentHash: hash, Path: path})
}

// SetEmbedder enables vector search: fragments are embedded on Ingest and
// VecSearch/HybridSearch become available. nil disables it.
func (s *Store) SetEmbedder(e *Embedder) { s.embedder = e }

// SetIdentifier enables document identity: each ingested document is captioned,
// summarised and typed by the model (identity.go), and the summary is indexed.
// nil disables it — an ingest then leaves whatever identity the document already
// had, because a caption is not invalidated by re-reading the same file.
func (s *Store) SetIdentifier(id *Identifier) { s.identifier = id }

// SetImageEmbedder enables IMAGE embeddings for figures (a CLIP-style tower):
// each figure is embedded from its image rather than its description. nil (the
// default) → figures embed their description with the text embedder instead.
func (s *Store) SetImageEmbedder(ie ImageEmbedder) { s.imageEmbedder = ie }

// schema is the whole index: metadata tables + an FTS5 mirror kept in sync by
// triggers (external-content pattern). The embedded sql/schema.sql is the SAME
// file sqlc reads for codegen — one source of truth, no drift. Every statement
// is IF NOT EXISTS, so re-applying on each Open is a no-op on an existing index.
//
//go:embed sql/schema.sql
var schema string

// Open opens (creating if needed) a raglit index at path. Use ":memory:" for a
// throwaway index (tests). foreign_keys is ON so a document delete cascades to
// its fragments; WAL keeps concurrent readers unblocked during ingest.
// AttachmentDirFor is where this index stores files extracted out of the archive
// at archivePath. Empty when the store has no home (":memory:", a bare Open),
// which is the one case extraction cannot run.
func (s *Store) AttachmentDirFor(archivePath string) string {
	if !s.withHome {
		return ""
	}
	return s.home.AttachmentDir(archivePath)
}

// LegacyAttachmentDirFor is the corpus sidecar an archive's attachments used to
// be written to, for readers that must work before the migration has run.
func (s *Store) LegacyAttachmentDirFor(archivePath string) string {
	return LegacyAttachmentDir(archivePath)
}

// SetExtractEmailAttachments turns mail-archive attachment extraction on.
func (s *Store) SetExtractEmailAttachments(v bool) { s.extractEmailAttachments = v }

func Open(path string) (*Store, error) {
	// foreign_keys is set in the DSN, NOT by db.Exec after opening.
	//
	// A PRAGMA is per-CONNECTION and database/sql keeps a POOL. Executing it once
	// after Open sets it on whichever connection happened to serve that call;
	// every connection the pool opens later — under concurrency, which ingest is
	// — starts with foreign_keys OFF. So ON DELETE CASCADE fired or did not
	// depending on which connection a statement landed on, and nothing about that
	// is deterministic or visible.
	//
	// What it cost: re-ingesting a document in an --embed index failed with
	// "UNIQUE constraint failed: fragment_vectors.fragment_id". commitDoc deletes
	// the document's fragments and relies on the cascade to take their vectors
	// with them; on a connection without the pragma the vectors survived, sqlite
	// reused the freed fragment rowid, and the new vector collided with the
	// orphan. Which made `--fresh` — the escape hatch for a bad read — the one
	// operation that could not be retried.
	//
	// busy_timeout rides the same DSN, and for a related reason: the daemon now
	// has more than one writer in the same index — the ingest worker and the
	// captioning worker — and a writer that arrives while another holds the lock
	// gets SQLITE_BUSY immediately unless told to wait. Ten seconds is far past
	// any write here, all of which are short.
	dsn := path
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// An in-memory index is PER CONNECTION: the pool opening a second one does
	// not find the first one's tables, it creates an empty database and answers
	// "no such table". That is invisible until something uses the store from more
	// than one goroutine — which the captioning worker does — so an in-memory
	// store is pinned to a single connection rather than left to fail later, in a
	// test, as a missing table nobody wrote a migration for.
	if strings.Contains(path, ":memory:") {
		db.SetMaxOpenConns(1)
	}
	// journal_mode is a DATABASE property, not a connection one — it persists in
	// the file — so setting it once here is correct and it stays correct.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("raglit: PRAGMA journal_mode=WAL: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("raglit: schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("raglit: migrate: %w", err)
	}
	if err := reclaimOrphanedJobs(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("raglit: reclaim: %w", err)
	}
	return &Store{db: db, q: gen.New(db), path: path}, nil
}

// reclaimOrphanedJobs aborts 'running' jobs whose owning process is gone.
//
// A job row records the state of work, but the work lives in a process, and the
// two part company the moment that process is killed: the row still says
// 'running' and `raglit status` still reports it in flight, with a stale ETA,
// forever. Three such rows survived a full daemon restart here and read as busy
// while the queue was in fact idle — which is worse than an error, because an
// error is something you go look at.
//
// Aborted rather than requeued, deliberately. A job that died mid-ingest may
// have been killed BY the document (an OOM on a huge scan), so silently retrying
// it on every daemon start is a crash loop. 'error' is visible, and `RetryJob`
// already accepts it, so requeueing stays a decision someone makes.
//
// Own-pid rows are left alone so a running daemon reopening its own index does
// not abort its own live work.
func reclaimOrphanedJobs(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, owner_pid FROM ingest_jobs WHERE state='running'`)
	if err != nil {
		return err
	}
	type orphan struct{ id, pid int64 }
	var orphans []orphan
	self := int64(os.Getpid())
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.pid); err != nil {
			rows.Close()
			return err
		}
		if o.pid == self || processAlive(o.pid) {
			continue
		}
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	now := time.Now().UnixNano()
	for _, o := range orphans {
		// pid 0 predates this column: the owner is unknown but provably not us.
		msg := fmt.Sprintf("aborted — worker pid %d is gone (job was left 'running')", o.pid)
		if o.pid == 0 {
			msg = "aborted — left 'running' by a process that exited before job ownership was recorded"
		}
		if _, err := db.Exec(
			`UPDATE ingest_jobs SET state='error', error=?, finished_at=? WHERE id=? AND state='running'`,
			msg, now, o.id); err != nil {
			return err
		}
	}
	return nil
}

// processAlive reports whether a pid is a live process. Signal 0 performs the
// existence and permission checks without delivering anything.
func processAlive(pid int64) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(int(pid))
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// migrate applies additive schema changes that CREATE TABLE IF NOT EXISTS can't
// (new columns on existing tables). Each step is idempotent: it checks for the
// column first, so a fresh DB (already carrying the column from schema) and an
// old DB converge without error.
func migrate(db *sql.DB) error {
	cols := []struct{ table, col, def string }{
		{"ingest_jobs", "mode", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "content_hash", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "frag_mode", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "frag_recipe", "TEXT NOT NULL DEFAULT ''"},
		{"fragments", "start_off", "INTEGER NOT NULL DEFAULT 0"},
		{"fragments", "end_off", "INTEGER NOT NULL DEFAULT 0"},
		{"fragments", "page_spans", "TEXT NOT NULL DEFAULT ''"},
		{"ingest_jobs", "owner_pid", "INTEGER NOT NULL DEFAULT 0"},
		// --fresh: re-read this document even if nothing about it changed.
		//
		// Added as a raw ALTER with no generated query, because `sqlc generate`
		// was corrupting the SQL text of every other query at the time. That was
		// never a property of sqlc: it is the rune/byte offset bug our fork
		// fixes, and it only fired because a comment held a multibyte character.
		// Codegen works — use `make generate`, never a bare `sqlc generate`.
		{"ingest_jobs", "fresh", "INTEGER NOT NULL DEFAULT 0"},
		// Scheduling lane (lane.go). An index that predates it has rows with an
		// empty lane, which no lane claims — backfilled by BackfillLanes, which
		// the daemon runs per index at startup.
		{"ingest_jobs", "lane", "TEXT NOT NULL DEFAULT ''"},
		{"page_readings", "note", "TEXT NOT NULL DEFAULT ''"},
		// Document identity (identity.go). An index that predates these reads as
		// "no caption yet", which is what `raglit identify` looks for.
		{"documents", "gen_name", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "gen_summary", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "gen_kind", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "gen_source", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "gen_model", "TEXT NOT NULL DEFAULT ''"},
		{"documents", "gen_at", "INTEGER NOT NULL DEFAULT 0"},
		{"documents", "gen_text_hash", "TEXT NOT NULL DEFAULT ''"},
		{"fragments", "origin", "TEXT NOT NULL DEFAULT ''"},
		// Which reader produced a reading (pagereadings.go). An index that
		// predates these has readings whose engine is simply unknown, which is
		// the truth about them.
		{"page_readings", "engine", "TEXT NOT NULL DEFAULT ''"},
		{"page_readings", "model", "TEXT NOT NULL DEFAULT ''"},
		{"ocr_pages", "model", "TEXT NOT NULL DEFAULT ''"},
		{"ocr_pages", "dpi", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range cols {
		has, err := hasColumn(db, c.table, c.col)
		if err != nil {
			return err
		}
		if !has {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, c.table, c.col, c.def)); err != nil {
				return err
			}
		}
	}
	// Indexes over MIGRATED columns belong here, after the ALTERs, and never in
	// schema.sql.
	//
	// schema.sql runs first on every open, and on an existing database its
	// CREATE TABLE IF NOT EXISTS is a no-op — so a column added by migration does
	// not exist yet when that file is applied, and an index naming one fails the
	// entire schema apply. Put there, this index took down every daemon with an
	// existing index: `schema: SQL logic error: no such column: lane`, on repeat,
	// because the failure is at open and systemd restarts it.
	//
	// This is the exact shape of the claim each lane makes (queue.go).
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS ingest_jobs_lane ON ingest_jobs(state, lane, id)`); err != nil {
		return err
	}
	return nil
}

// hasColumn reports whether table has a column named col.
func hasColumn(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// OpenHome opens the home's primary ("default") index. Use Open for a raw path
// or an in-memory test index; OpenIndex for a named index within the home.
func OpenHome(home Home) (*Store, error) {
	return OpenIndex(home, "default")
}

// OpenIndex opens a NAMED index within a home (sharing its originals/pages), so
// one home can hold several indexes. "default" (or "") is the home's primary
// index.sqlite; any other name is index-<name>.sqlite. Created if absent.
// Ingesting a doc whose Path is a real file copies it into <home>/originals/.
func OpenIndex(home Home, name string) (*Store, error) {
	if err := home.Ensure(); err != nil {
		return nil, err
	}
	s, err := Open(home.indexPath(name))
	if err != nil {
		return nil, err
	}
	s.home = home
	s.withHome = true
	// Config decides the sidecars raglit may write into the corpus — the
	// transcription and the attachment directory — per index if it says so and
	// project-wide otherwise. Read here because this is where a Store learns which
	// home and index it is — the flag existed and was never consulted, which is
	// the same silent gap that made three earlier fixes no-ops.
	if cfg, _, err := LoadConfig(home); err == nil {
		s.extractEmailAttachments = cfg.ExtractEmailAttachments
		key := name
		if key == "" {
			key = "default"
		}
		if ic, ok := cfg.Indexes[key]; ok {
			if ic.ExtractEmailAttachments {
				s.extractEmailAttachments = true
			}
		}
	}
	return s, nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Fragment is one indexable unit at the document:page:fragment grain.
type Fragment struct {
	Page int    // 1-based page number (0 for pageless sources like plain text)
	Ord  int    // fragment order within the page
	Text string // the searchable text
}

// Document is a source doc plus its fragments. Path is the unique key.
type Document struct {
	Path      string
	Title     string
	Fragments []Fragment
}

// Ingest upserts a document and (re)indexes its fragments in one transaction.
// Re-ingesting the same Path is idempotent: the doc's old fragments are dropped
// and replaced, so re-running an index over a changed file converges rather
// than duplicating. Empty-text fragments are skipped.
func (s *Store) Ingest(ctx context.Context, doc Document) error {
	if doc.Path == "" {
		return fmt.Errorf("raglit: ingest: empty path")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	q := gq(tx)

	docID, err := q.UpsertDocument(ctx, gen.UpsertDocumentParams{Path: doc.Path, Title: doc.Title, AddedAt: time.Now().UnixNano()})
	if err != nil {
		return fmt.Errorf("raglit: upsert document: %w", err)
	}
	// Replace-on-reingest: drop old fragments (triggers clean the fts mirror;
	// FK cascade drops their vectors).
	if err := q.DeleteFragmentsByDoc(ctx, docID); err != nil {
		return fmt.Errorf("raglit: clear fragments: %w", err)
	}
	type frag struct {
		id   int64
		text string
	}
	var frags []frag
	for _, f := range doc.Fragments {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		id, err := q.InsertFragment(ctx, gen.InsertFragmentParams{DocID: docID, Page: int64(f.Page), Ord: int64(f.Ord), Text: f.Text})
		if err != nil {
			return fmt.Errorf("raglit: insert fragment: %w", err)
		}
		frags = append(frags, frag{id, f.Text})
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Vector tier (opt-in): embed the fresh fragments and store their vectors.
	// Done AFTER commit so the network round-trip doesn't hold the write tx.
	if s.embedder != nil && len(frags) > 0 {
		texts := make([]string, len(frags))
		for i, f := range frags {
			texts[i] = f.text
		}
		vecs, err := s.embedder.EmbedDocs(ctx, texts)
		if err != nil {
			return fmt.Errorf("raglit: embed fragments: %w", err)
		}
		vtx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer vtx.Rollback() //nolint:errcheck
		vq := gq(vtx)
		for i, f := range frags {
			if i >= len(vecs) {
				break
			}
			if err := vq.InsertVector(ctx, gen.InsertVectorParams{FragmentID: f.id, Dim: int64(len(vecs[i])), Vec: encodeVec(vecs[i])}); err != nil {
				return fmt.Errorf("raglit: store vector: %w", err)
			}
		}
		if err := vtx.Commit(); err != nil {
			return err
		}
	}
	// Keep a copy of the source so the index is self-contained (a home store
	// only; skipped for synthetic docs whose Path isn't a real file).
	if s.withHome {
		if err := s.storeOriginal(doc.Path); err != nil {
			return fmt.Errorf("raglit: store original: %w", err)
		}
	}
	return nil
}

// storeOriginal copies doc's source file into <home>/originals/ if it exists and
// isn't already stored. A non-file path (synthetic ingest) is a no-op.
func (s *Store) storeOriginal(docPath string) error {
	fi, err := os.Stat(docPath)
	if err != nil || fi.IsDir() {
		return nil //nolint:nilerr // not a real file → nothing to store
	}
	dst := s.home.OriginalPath(docPath)
	if _, err := os.Stat(dst); err == nil {
		return nil // already stored (deterministic path)
	}
	in, err := os.Open(docPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// IngestPDF pagifies an image/scanned PDF and indexes it via LLM segmentation:
// the vision model (ocr's client) reads each page image and carves coherent
// fragments, with open fragments stitched across page boundaries and vectors
// embedded concurrently. Page images are written under the home's pages/ dir
// when the store has a home, else a temp dir. Returns the number of fragments
// indexed.
func (s *Store) IngestPDF(ctx context.Context, ocr *OCR, pdfPath string) (int, error) {
	n, _, err := s.ingestPDF(ctx, nil, ocr, pdfPath, pdfPath, filepath.Base(pdfPath), FragConfig{}, nil)
	return n, err
}

// ingestPDF is IngestPDF with the document identity (docPath, title) decoupled
// from the file on disk (filePath) — so a queued URL job can process a temp file
// while keeping the URL as the stable document key. sl records the extract stage
// (and the downstream ocr/segment/embed/commit stages via ingestUnits).
// sg is the segmenter to use; nil means "build one on the OCR client", which is
// what this always did. Passed in rather than derived because the segmenter's
// model is separately configurable — deriving it here is exactly how a
// configured segment model got silently ignored on the path every PDF takes.
func (s *Store) ingestPDF(ctx context.Context, sg *Segmenter, ocr *OCR, docPath, filePath, title string, fc FragConfig, sl *StageLog) (int, string, error) {
	// Per-page hybrid: text-layer pages become text units (free, exact), scanned
	// pages become image units for the OCR path. Replaces the old Pagify-only path,
	// which saw no text layer and failed on born-digital PDFs (ErrNoPageImages).
	units, err := pdfUnits(ctx, filePath, ocr != nil, cheapOf(ocr), renderOf(ocr))
	if err != nil {
		sl.Fail("extract", "pdf", err)
		return 0, "", err
	}
	imgPages := 0
	for _, u := range units {
		if u.isImage() {
			imgPages++
		}
	}
	sl.Done("extract", "pdf", fmt.Sprintf("%d page(s): %d text-layer, %d scanned", len(units), len(units)-imgPages, imgPages))
	if sg == nil && ocr != nil {
		sg = NewSegmenter(ocr.Client)
	}
	return s.ingestUnits(ctx, sg, ocr, docPath, title, units, fc, sl)
}

// Hit is one BM25-ranked fragment. Score is normalized so HIGHER is better
// (the opposite of SQLite's raw bm25(), which returns more-negative for better
// matches) — matching agentkit's DocHit.Score convention.
type Hit struct {
	ID    int64 // fragment id (stable key for fusing rankings)
	Path  string
	Title string
	Page  int
	Ord   int
	Text  string
	Score float64
	// Origin is empty for the document's own words, and names what a machine
	// made otherwise: "identity" for a generated caption/summary (identity.go),
	// "described" for a model's account of an IMAGE (indextext.go) — the 700
	// words chandra writes about a photograph, naming a car's make and its
	// licence plate, none of which anybody wrote down.
	//
	// These rank in the same list on purpose: a summary is how a document whose
	// body never says "purchase and sale agreement" becomes findable by that
	// query, and a description is the only way a photograph is findable at all.
	// But they are a machine's words, so every renderer says so and nothing
	// quotes from them.
	Origin string
}

// IsDescription reports whether this hit is a DESCRIPTION of a document — its
// caption and summary — rather than text from the document itself. True for a
// machine's and for a person's alike: neither is the instrument, and neither can
// be quoted as if it were.
func (h Hit) IsDescription() bool { return h.Origin != "" }

// pathPredicate returns a bare SQL predicate + its arg constraining d.path to
// documents whose path STARTS WITH pathPrefix (a subtree scope), or ("", nil) for
// an empty prefix. instr(path, prefix)=1 is an exact prefix match with no LIKE
// wildcard/escaping surprises; pass a trailing "/" for a clean directory subtree.
func pathPredicate(pathPrefix string) (string, []any) {
	if pathPrefix == "" {
		return "", nil
	}
	return "instr(d.path, ?) = 1", []any{pathPrefix}
}

// searchLocal runs a BM25 query and returns up to limit fragments, best first,
// optionally constrained to a path subtree (pathPrefix). The query is tokenized
// and OR-combined for recall — BM25 still floats the strongest matches to the top,
// and the ambient/notify use case wants recall over precision. Returns no error on
// zero matches (empty slice).
func (s *Store) searchLocal(query, pathPrefix string, limit int) ([]Hit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	pred, pargs := pathPredicate(pathPrefix)
	if pred != "" {
		pred = " AND " + pred
	}
	args := append([]any{match}, pargs...)
	args = append(args, limit)
	rows, err := s.db.Query(
		`SELECT f.id, d.path, d.title, f.page, f.ord, f.text, f.page_spans, f.origin, bm25(fragments_fts) AS score
		 FROM fragments_fts
		 JOIN fragments f ON f.id = fragments_fts.rowid
		 JOIN documents d ON d.id = f.doc_id
		 WHERE fragments_fts MATCH ?`+pred+`
		 ORDER BY score
		 LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("raglit: search: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		var bm25 float64
		var spans string
		if err := rows.Scan(&h.ID, &h.Path, &h.Title, &h.Page, &h.Ord, &h.Text, &spans, &h.Origin, &bm25); err != nil {
			return nil, err
		}
		// A fragment can cross page boundaries; f.page is only where it started.
		// Resolve the page the match is actually on — see hitpage.go.
		h.Page = HitPage(h.Page, spans, h.Text, query)
		h.Score = -bm25 // flip so higher = better
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// VecSearch is VecSearchPath with no path constraint.
func (s *Store) VecSearch(ctx context.Context, query string, limit int) ([]Hit, error) {
	return s.VecSearchPath(ctx, query, "", limit)
}

// VecSearchPath embeds the query and ranks fragments by cosine similarity, best
// first, optionally constrained to a path subtree (pathPrefix). Brute-force: it
// scans every stored vector in scope (fine for a local corpus; see embed.go).
// Requires SetEmbedder. Score is cosine in [-1,1] (higher = better). Fragments
// without a vector (indexed before embeddings were enabled) are invisible.
func (s *Store) VecSearchPath(ctx context.Context, query, pathPrefix string, limit int) ([]Hit, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("raglit: VecSearch needs an embedder (SetEmbedder)")
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	qv, err := s.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	pred, pargs := pathPredicate(pathPrefix)
	where := ""
	if pred != "" {
		where = " WHERE " + pred
	}
	rows, err := s.db.Query(
		`SELECT f.id, d.path, d.title, f.page, f.ord, f.text, f.page_spans, f.origin, fv.vec
		 FROM fragment_vectors fv
		 JOIN fragments f ON f.id = fv.fragment_id
		 JOIN documents d ON d.id = f.doc_id`+where, pargs...)
	if err != nil {
		return nil, fmt.Errorf("raglit: vecsearch: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		var blob []byte
		var spans string
		if err := rows.Scan(&h.ID, &h.Path, &h.Title, &h.Page, &h.Ord, &h.Text, &spans, &h.Origin, &blob); err != nil {
			return nil, err
		}
		// A vector hit has no literal terms to locate, so this usually falls back
		// to the fragment's start page. It is wired anyway: a semantic query whose
		// words DO appear gets the right page, and the two search paths must not
		// cite the same fragment differently.
		h.Page = HitPage(h.Page, spans, h.Text, query)
		h.Score = float64(dot(qv, decodeVec(blob)))
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// HybridSearch is HybridSearchPath with no path constraint.
func (s *Store) HybridSearch(ctx context.Context, query string, limit int) ([]Hit, error) {
	return s.HybridSearchPath(ctx, query, "", limit)
}

// HybridSearchPath fuses BM25 and vector rankings with Reciprocal Rank Fusion
// (RRF) — the standard, score-scale-agnostic combiner: a fragment's fused score
// is the sum over each ranked list of 1/(rrfK + rank). It over-fetches from
// each side, so a fragment strong on either signal surfaces. Both sides honor the
// optional path subtree scope. Requires an embedder. Returns up to limit
// fragments, best fused first.
func (s *Store) HybridSearchPath(ctx context.Context, query, pathPrefix string, limit int) ([]Hit, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("raglit: HybridSearch needs an embedder (SetEmbedder)")
	}
	if limit <= 0 {
		limit = 10
	}
	pool := limit * 4
	lex, err := s.SearchPath(query, pathPrefix, pool)
	if err != nil {
		return nil, err
	}
	vec, err := s.VecSearchPath(ctx, query, pathPrefix, pool)
	if err != nil {
		return nil, err
	}
	const rrfK = 60.0
	fused := map[int64]*Hit{}
	score := map[int64]float64{}
	add := func(list []Hit) {
		for rank, h := range list {
			if _, ok := fused[h.ID]; !ok {
				hc := h
				fused[h.ID] = &hc
			}
			score[h.ID] += 1.0 / (rrfK + float64(rank))
		}
	}
	add(lex)
	add(vec)

	out := make([]Hit, 0, len(fused))
	for id, h := range fused {
		h.Score = score[id]
		out = append(out, *h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ftsQuery turns arbitrary user text into a safe FTS5 MATCH expression: each
// whitespace token is double-quoted (FTS5 string literal — internal quotes
// doubled), OR-joined. Quoting neutralizes FTS5 operators/punctuation in user
// input ("what's", "a-b", "OR") that would otherwise be a syntax error.
func ftsQuery(q string) string {
	var quoted []string
	for _, tok := range strings.Fields(q) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}
