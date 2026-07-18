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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a handle to one raglit index file. When opened via OpenHome it also
// knows a Home layout and copies ingested originals into it.
type Store struct {
	db       *sql.DB
	path     string
	home     Home
	withHome bool
	embedder *Embedder // nil → lexical only; set for vector/hybrid search
}

// Path is the index file path (or ":memory:").
func (s *Store) Path() string { return s.path }

// SetEmbedder enables vector search: fragments are embedded on Ingest and
// VecSearch/HybridSearch become available. nil disables it.
func (s *Store) SetEmbedder(e *Embedder) { s.embedder = e }

// schema is the whole index: metadata tables + an FTS5 mirror kept in sync by
// triggers (external-content pattern — the fts table stores only the index, not
// a second copy of the text).
const schema = `
CREATE TABLE IF NOT EXISTS documents (
  id       INTEGER PRIMARY KEY,
  path     TEXT NOT NULL UNIQUE,
  title    TEXT NOT NULL DEFAULT '',
  added_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS fragments (
  id     INTEGER PRIMARY KEY,
  doc_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page   INTEGER NOT NULL DEFAULT 0,
  ord    INTEGER NOT NULL DEFAULT 0,
  text   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS fragments_doc ON fragments(doc_id);
CREATE VIRTUAL TABLE IF NOT EXISTS fragments_fts USING fts5(
  text,
  content='fragments',
  content_rowid='id',
  tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS fragments_ai AFTER INSERT ON fragments BEGIN
  INSERT INTO fragments_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS fragments_ad AFTER DELETE ON fragments BEGIN
  INSERT INTO fragments_fts(fragments_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS fragments_au AFTER UPDATE ON fragments BEGIN
  INSERT INTO fragments_fts(fragments_fts, rowid, text) VALUES ('delete', old.id, old.text);
  INSERT INTO fragments_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TABLE IF NOT EXISTS fragment_vectors (
  fragment_id INTEGER PRIMARY KEY REFERENCES fragments(id) ON DELETE CASCADE,
  dim         INTEGER NOT NULL,
  vec         BLOB NOT NULL   -- little-endian float32, L2-normalized
);
CREATE TABLE IF NOT EXISTS ingest_jobs (
  id          INTEGER PRIMARY KEY,
  url         TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  state       TEXT NOT NULL DEFAULT 'pending',  -- pending | running | done | error
  error       TEXT NOT NULL DEFAULT '',
  fragments   INTEGER NOT NULL DEFAULT 0,
  enqueued_at INTEGER NOT NULL,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ingest_jobs_state ON ingest_jobs(state, id);
`

// Open opens (creating if needed) a raglit index at path. Use ":memory:" for a
// throwaway index (tests). foreign_keys is ON so a document delete cascades to
// its fragments; WAL keeps concurrent readers unblocked during ingest.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("raglit: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("raglit: schema: %w", err)
	}
	return &Store{db: db, path: path}, nil
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

	if _, err := tx.Exec(
		`INSERT INTO documents(path, title, added_at) VALUES(?,?,?)
		 ON CONFLICT(path) DO UPDATE SET title=excluded.title, added_at=excluded.added_at`,
		doc.Path, doc.Title, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("raglit: upsert document: %w", err)
	}
	var docID int64
	if err := tx.QueryRow(`SELECT id FROM documents WHERE path=?`, doc.Path).Scan(&docID); err != nil {
		return fmt.Errorf("raglit: doc id: %w", err)
	}
	// Replace-on-reingest: drop old fragments (triggers clean the fts mirror;
	// FK cascade drops their vectors).
	if _, err := tx.Exec(`DELETE FROM fragments WHERE doc_id=?`, docID); err != nil {
		return fmt.Errorf("raglit: clear fragments: %w", err)
	}
	ins, err := tx.Prepare(`INSERT INTO fragments(doc_id, page, ord, text) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	type frag struct {
		id   int64
		text string
	}
	var frags []frag
	for _, f := range doc.Fragments {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		res, err := ins.Exec(docID, f.Page, f.Ord, f.Text)
		if err != nil {
			return fmt.Errorf("raglit: insert fragment: %w", err)
		}
		id, _ := res.LastInsertId()
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
		vins, err := vtx.Prepare(`INSERT INTO fragment_vectors(fragment_id, dim, vec) VALUES(?,?,?)`)
		if err != nil {
			return err
		}
		defer vins.Close()
		for i, f := range frags {
			if i >= len(vecs) {
				break
			}
			if _, err := vins.Exec(f.id, len(vecs[i]), encodeVec(vecs[i])); err != nil {
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
	return s.ingestPDF(ctx, ocr, pdfPath, pdfPath, filepath.Base(pdfPath))
}

// ingestPDF is IngestPDF with the document identity (docPath, title) decoupled
// from the file on disk (filePath) — so a queued URL job can process a temp file
// while keeping the URL as the stable document key.
func (s *Store) ingestPDF(ctx context.Context, ocr *OCR, docPath, filePath, title string) (int, error) {
	outDir := ""
	if s.withHome {
		outDir = s.home.PageDir(docPath)
	} else {
		tmp, err := os.MkdirTemp("", "raglit-pages-")
		if err != nil {
			return 0, err
		}
		defer os.RemoveAll(tmp)
		outDir = tmp
	}
	pages, err := Pagify(filePath, outDir)
	if err != nil {
		return 0, err
	}
	units := make([]ingestUnit, 0, len(pages))
	for _, p := range pages {
		units = append(units, ingestUnit{page: p.Page, mime: p.Mime, data: p.Data})
	}
	return s.ingestUnits(ctx, NewSegmenter(ocr.Client), docPath, title, units)
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
}

// Search runs a BM25 query and returns up to limit fragments, best first. The
// query is tokenized and OR-combined for recall — BM25 still floats the
// strongest matches to the top, and the ambient/notify use case wants recall
// over precision. Returns no error on zero matches (empty slice).
func (s *Store) Search(query string, limit int) ([]Hit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(
		`SELECT f.id, d.path, d.title, f.page, f.ord, f.text, bm25(fragments_fts) AS score
		 FROM fragments_fts
		 JOIN fragments f ON f.id = fragments_fts.rowid
		 JOIN documents d ON d.id = f.doc_id
		 WHERE fragments_fts MATCH ?
		 ORDER BY score
		 LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("raglit: search: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		var bm25 float64
		if err := rows.Scan(&h.ID, &h.Path, &h.Title, &h.Page, &h.Ord, &h.Text, &bm25); err != nil {
			return nil, err
		}
		h.Score = -bm25 // flip so higher = better
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// VecSearch embeds the query and ranks fragments by cosine similarity, best
// first. Brute-force: it scans every stored vector (fine for a local corpus;
// see embed.go). Requires SetEmbedder. Score is cosine in [-1,1] (higher =
// better). Fragments without a vector (indexed before embeddings were enabled)
// are invisible to this search.
func (s *Store) VecSearch(ctx context.Context, query string, limit int) ([]Hit, error) {
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
	rows, err := s.db.Query(
		`SELECT f.id, d.path, d.title, f.page, f.ord, f.text, fv.vec
		 FROM fragment_vectors fv
		 JOIN fragments f ON f.id = fv.fragment_id
		 JOIN documents d ON d.id = f.doc_id`)
	if err != nil {
		return nil, fmt.Errorf("raglit: vecsearch: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		var blob []byte
		if err := rows.Scan(&h.ID, &h.Path, &h.Title, &h.Page, &h.Ord, &h.Text, &blob); err != nil {
			return nil, err
		}
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

// HybridSearch fuses BM25 and vector rankings with Reciprocal Rank Fusion
// (RRF) — the standard, score-scale-agnostic combiner: a fragment's fused score
// is the sum over each ranked list of 1/(rrfK + rank). It over-fetches from
// each side, so a fragment strong on either signal surfaces. Requires an
// embedder. Returns up to limit fragments, best fused first.
func (s *Store) HybridSearch(ctx context.Context, query string, limit int) ([]Hit, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("raglit: HybridSearch needs an embedder (SetEmbedder)")
	}
	if limit <= 0 {
		limit = 10
	}
	pool := limit * 4
	lex, err := s.Search(query, pool)
	if err != nil {
		return nil, err
	}
	vec, err := s.VecSearch(ctx, query, pool)
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
