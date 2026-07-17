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
}

// Path is the index file path (or ":memory:").
func (s *Store) Path() string { return s.path }

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

// OpenHome opens the index inside a Home layout (creating the layout if absent)
// and enables original-file storage: ingesting a document whose Path is a real
// file copies it into <home>/originals/. Use this for the CLI; use Open for a
// raw path or an in-memory test index.
func OpenHome(home Home) (*Store, error) {
	if err := home.Ensure(); err != nil {
		return nil, err
	}
	s, err := Open(home.IndexPath())
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
func (s *Store) Ingest(doc Document) error {
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
	// Replace-on-reingest: drop old fragments (triggers clean the fts mirror).
	if _, err := tx.Exec(`DELETE FROM fragments WHERE doc_id=?`, docID); err != nil {
		return fmt.Errorf("raglit: clear fragments: %w", err)
	}
	ins, err := tx.Prepare(`INSERT INTO fragments(doc_id, page, ord, text) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, f := range doc.Fragments {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		if _, err := ins.Exec(docID, f.Page, f.Ord, f.Text); err != nil {
			return fmt.Errorf("raglit: insert fragment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
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

// IngestPDF pagifies an image/scanned PDF, OCRs each page through ocr, and
// indexes the transcribed text with real page numbers (one fragment per page).
// Page images are written under the home's pages/ dir when the store has a home
// (so they persist for re-OCR/inspection), else to a temp dir that is cleaned
// up. Returns the number of pages indexed. A page whose OCR is blank is skipped.
func (s *Store) IngestPDF(ctx context.Context, ocr *OCR, pdfPath string) (int, error) {
	outDir := ""
	if s.withHome {
		outDir = s.home.PageDir(pdfPath)
	} else {
		tmp, err := os.MkdirTemp("", "raglit-pages-")
		if err != nil {
			return 0, err
		}
		defer os.RemoveAll(tmp)
		outDir = tmp
	}
	pages, err := Pagify(pdfPath, outDir)
	if err != nil {
		return 0, err
	}
	doc := Document{Path: pdfPath, Title: filepath.Base(pdfPath)}
	for _, p := range pages {
		text, err := ocr.Page(ctx, p)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		doc.Fragments = append(doc.Fragments, Fragment{Page: p.Page, Ord: 0, Text: text})
	}
	if err := s.Ingest(doc); err != nil {
		return 0, err
	}
	return len(doc.Fragments), nil
}

// Hit is one BM25-ranked fragment. Score is normalized so HIGHER is better
// (the opposite of SQLite's raw bm25(), which returns more-negative for better
// matches) — matching agentkit's DocHit.Score convention.
type Hit struct {
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
		`SELECT d.path, d.title, f.page, f.ord, f.text, bm25(fragments_fts) AS score
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
		if err := rows.Scan(&h.Path, &h.Title, &h.Page, &h.Ord, &h.Text, &bm25); err != nil {
			return nil, err
		}
		h.Score = -bm25 // flip so higher = better
		hits = append(hits, h)
	}
	return hits, rows.Err()
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
