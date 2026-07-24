package raglit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Shared document pool — cross-index dedup of INDEXING work.
//
// The expensive part of ingest (extract/OCR/segment/embed) is a pure function of
// the source bytes and the "recipe" (the models + config that shape the output).
// The pool caches that output keyed by (recipe_hash, file_hash), SHARED across
// every index in a daemon. Ingesting the same file — into any index, or on a
// retry — copies the cached fragments + vectors + page images instead of
// re-running the LLM. Re-indexing under a different recipe (alt models) is a new
// key, so it reprocesses; a retry with the same recipe reuses. Document grain,
// not fragment grain.

// PooledFragment is one cached fragment: its text and (optional) vector.
type PooledFragment struct {
	Page int       `json:"page"`
	Ord  int       `json:"ord"`
	Text string    `json:"text"`
	Vec  []float32 `json:"vec,omitempty"`
}

// PooledPage is one cached page's provenance. Image is an absolute source path on
// export, rewritten to a pool-pages basename when stored.
type PooledPage struct {
	Page   int    `json:"page"`
	Engine string `json:"engine"`
	Image  string `json:"image,omitempty"`
}

// PooledDoc is a fully-processed document: the reusable output of one ingest.
type PooledDoc struct {
	Title     string           `json:"title"`
	Fragments []PooledFragment `json:"fragments"`
	Pages     []PooledPage     `json:"pages"`
}

const poolSchema = `
CREATE TABLE IF NOT EXISTS pool (
  recipe_hash  TEXT NOT NULL,
  file_hash    TEXT NOT NULL,
  payload      BLOB NOT NULL,   -- JSON PooledDoc (page images live in pool-pages/)
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,  -- updated on reuse; the GC/LRU basis
  PRIMARY KEY (recipe_hash, file_hash)
);`

// Pool is the shared processed-document cache (pool.sqlite + pool-pages/) under a
// daemon root.
type Pool struct {
	db        *sql.DB
	pagesRoot string
}

// OpenPool opens (creating if needed) the pool under root: <root>/pool.sqlite and
// <root>/pool-pages/.
func OpenPool(root string) (*Pool, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "pool.sqlite"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(poolSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("raglit: pool schema: %w", err)
	}
	// Migrate an older pool (no last_used_at): add it, seed from created_at so
	// existing entries aren't treated as never-used (and instantly GC'd).
	if has, _ := hasColumn(db, "pool", "last_used_at"); !has {
		if _, err := db.Exec(`ALTER TABLE pool ADD COLUMN last_used_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			db.Close()
			return nil, err
		}
		db.Exec(`UPDATE pool SET last_used_at = created_at WHERE last_used_at = 0`) //nolint:errcheck
	}
	return &Pool{db: db, pagesRoot: filepath.Join(root, "pool-pages")}, nil
}

// Close releases the pool database.
func (p *Pool) Close() error { return p.db.Close() }

// FileDir is where a file's pooled page images live.
func (p *Pool) FileDir(fileHash string) string { return filepath.Join(p.pagesRoot, fileHash) }

// Get returns the cached processed document for (recipe, file), if present.
func (p *Pool) Get(recipeHash, fileHash string) (PooledDoc, bool, error) {
	var payload []byte
	err := p.db.QueryRow(`SELECT payload FROM pool WHERE recipe_hash=? AND file_hash=?`, recipeHash, fileHash).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return PooledDoc{}, false, nil
	}
	if err != nil {
		return PooledDoc{}, false, err
	}
	var doc PooledDoc
	if err := json.Unmarshal(payload, &doc); err != nil {
		return PooledDoc{}, false, err
	}
	// Record the reuse for LRU eviction.
	p.db.Exec(`UPDATE pool SET last_used_at=? WHERE recipe_hash=? AND file_hash=?`, //nolint:errcheck
		time.Now().UnixNano(), recipeHash, fileHash)
	return doc, true, nil
}

// Put caches a processed document. Page images (doc.Pages[].Image = absolute
// source paths) are copied into pool-pages/<file>/ and rewritten to basenames.
func (p *Pool) Put(recipeHash, fileHash string, doc PooledDoc) error {
	if len(doc.Pages) > 0 {
		dir := p.FileDir(fileHash)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for i := range doc.Pages {
			src := doc.Pages[i].Image
			if src == "" {
				continue
			}
			base := fmt.Sprintf("p%04d%s", doc.Pages[i].Page, filepath.Ext(src))
			data, err := os.ReadFile(src)
			if err != nil {
				doc.Pages[i].Image = "" // source gone; keep engine, drop image
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, base), data, 0o644); err != nil {
				return err
			}
			doc.Pages[i].Image = base
		}
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	_, err = p.db.Exec(
		`INSERT INTO pool(recipe_hash, file_hash, payload, created_at, last_used_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(recipe_hash, file_hash) DO UPDATE SET payload=excluded.payload, created_at=excluded.created_at, last_used_at=excluded.last_used_at`,
		recipeHash, fileHash, payload, now, now)
	return err
}

// PoolStats is a snapshot of the pool for reporting.
type PoolStats struct {
	Entries int `json:"entries"` // cached (recipe, file) documents
	Files   int `json:"files"`   // distinct source files (pool-pages dirs)
}

// Stats reports the pool's size.
func (p *Pool) Stats() (PoolStats, error) {
	var st PoolStats
	if err := p.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT file_hash) FROM pool`).Scan(&st.Entries, &st.Files); err != nil {
		return st, err
	}
	return st, nil
}

// GC evicts pooled documents: any not reused within maxAgeUnused (0 = disabled),
// and — over maxEntries (0 = unlimited) — the least-recently-used beyond the cap.
// A file's pool-pages/ dir is removed once no entry references it. Returns the
// number of entries evicted.
func (p *Pool) GC(maxAgeUnused time.Duration, maxEntries int) (int, error) {
	type key struct{ recipe, file string }
	evict := map[key]bool{}
	files := map[string]bool{}

	collect := func(q string, args ...any) error {
		rows, err := p.db.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.recipe, &k.file); err != nil {
				return err
			}
			evict[k] = true
			files[k.file] = true
		}
		return rows.Err()
	}

	if maxAgeUnused > 0 {
		cutoff := time.Now().UnixNano() - int64(maxAgeUnused)
		if err := collect(`SELECT recipe_hash, file_hash FROM pool WHERE last_used_at < ?`, cutoff); err != nil {
			return 0, err
		}
	}
	if maxEntries > 0 {
		// Everything past the newest-used maxEntries.
		if err := collect(`SELECT recipe_hash, file_hash FROM pool ORDER BY last_used_at DESC LIMIT -1 OFFSET ?`, maxEntries); err != nil {
			return 0, err
		}
	}
	if len(evict) == 0 {
		return 0, nil
	}
	for k := range evict {
		if _, err := p.db.Exec(`DELETE FROM pool WHERE recipe_hash=? AND file_hash=?`, k.recipe, k.file); err != nil {
			return 0, err
		}
	}
	// Drop pool-pages dirs for files no entry references any more.
	for f := range files {
		var n int
		if err := p.db.QueryRow(`SELECT COUNT(*) FROM pool WHERE file_hash=?`, f).Scan(&n); err != nil {
			return 0, err
		}
		if n == 0 {
			os.RemoveAll(p.FileDir(f)) //nolint:errcheck
		}
	}
	return len(evict), nil
}

// ExportDoc reads a freshly-ingested document back out of an index as a PooledDoc
// (fragments + vectors + page provenance, page Image = absolute source path), for
// caching in the pool.
func (s *Store) ExportDoc(path string) (PooledDoc, error) {
	ctx := context.Background()
	doc, err := s.q.GetDocumentByPath(ctx, path)
	if err != nil {
		return PooledDoc{}, err
	}
	out := PooledDoc{Title: doc.Title}
	frows, err := s.q.ExportFragments(ctx, doc.ID)
	if err != nil {
		return PooledDoc{}, err
	}
	for _, r := range frows {
		pf := PooledFragment{Page: int(r.Page), Ord: int(r.Ord), Text: r.Text}
		if len(r.Vec) > 0 {
			pf.Vec = decodeVec(r.Vec)
		}
		out.Fragments = append(out.Fragments, pf)
	}
	prows, err := s.q.ListOcrPagesByDoc(ctx, doc.ID)
	if err != nil {
		return PooledDoc{}, err
	}
	for _, r := range prows {
		out.Pages = append(out.Pages, PooledPage{Page: int(r.Page), Engine: r.Engine, Image: r.ImagePath})
	}
	return out, nil
}

// IngestPooled writes a cached PooledDoc into this index — the CHEAP reuse path
// (no LLM/OCR/embed): fragments + their cached vectors, and page provenance with
// each image copied from poolFileDir into this index's pages/. Atomic (commitDoc).
func (s *Store) IngestPooled(ctx context.Context, docPath, title string, doc PooledDoc, poolFileDir string) (int, error) {
	frags := make([]stagedFrag, len(doc.Fragments))
	vecs := map[int][]float32{}
	for i, f := range doc.Fragments {
		frags[i] = stagedFrag{page: f.Page, ord: f.Ord, text: f.Text}
		if len(f.Vec) > 0 {
			vecs[i] = f.Vec
		}
	}
	prov := make([]stagedPage, 0, len(doc.Pages))
	for _, p := range doc.Pages {
		imgPath := ""
		if p.Image != "" && poolFileDir != "" {
			if dst, err := s.copyPageImageFrom(docPath, p.Page, filepath.Join(poolFileDir, p.Image)); err == nil {
				imgPath = dst
			}
		}
		prov = append(prov, stagedPage{page: p.Page, engine: p.Engine, imgPath: imgPath})
	}
	if err := s.commitDoc(docPath, title, frags, prov, vecs); err != nil {
		return 0, err
	}
	return len(frags), nil
}

// copyPageImageFrom copies a pooled page image into this index's pages/ dir.
func (s *Store) copyPageImageFrom(docPath string, page int, src string) (string, error) {
	if !s.withHome {
		return "", nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	mime := "image/png"
	if ext := filepath.Ext(src); ext == ".jpg" || ext == ".jpeg" {
		mime = "image/jpeg"
	}
	return s.savePageImage(docPath, page, mime, data)
}
