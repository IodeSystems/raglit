package raglit

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OCR review + document inspection.
//
// Ingest records per-page provenance in ocr_pages (see pipeline.go): which
// engine produced a page ("text" for a born-digital/plain page, "vision" for a
// page the vision model OCR'd during segmentation) and the saved page image for
// image pages. The daemon's review UI reads these back so a human can eyeball
// OCR quality — page image beside the indexed text — and see which pages cost a
// VLM call. On-demand re-OCR (daemon) reruns the cheap→gate→VLM cascade against
// a saved page image to expose the escalation decision.

// savePageImage writes a page image under <home>/pages/<tag>/pNNN.<ext> and
// returns its absolute path. A no-home store (raw --db) saves nothing (""), so
// review then shows the engine/text without an image.
func (s *Store) savePageImage(docPath string, page int, mime string, data []byte) (string, error) {
	if !s.withHome {
		return "", nil
	}
	dir := s.home.PageDir(docPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := ".png"
	if strings.Contains(mime, "jpeg") || strings.Contains(mime, "jpg") {
		ext = ".jpg"
	}
	p := filepath.Join(dir, fmt.Sprintf("p%04d%s", page, ext))
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// recordPage upserts one page's provenance (idempotent on reingest).
func (s *Store) recordPage(docID int64, page int, engine, imagePath string) error {
	_, err := s.db.Exec(
		`INSERT INTO ocr_pages(doc_id, page, engine, image_path) VALUES(?,?,?,?)
		 ON CONFLICT(doc_id, page) DO UPDATE SET engine=excluded.engine, image_path=excluded.image_path`,
		docID, page, engine, imagePath)
	return err
}

// DocSummary is a document with counts for the review UI's document list.
type DocSummary struct {
	Path      string         `json:"path"`
	Title     string         `json:"title"`
	Fragments int            `json:"fragments"`
	Pages     int            `json:"pages"`    // OCR-tracked pages (page ≥ 1)
	Vision    int            `json:"vision"`   // pages that used the VLM
	Engines   map[string]int `json:"engines"`  // engine → page count
	AddedAt   int64          `json:"added_at"`
}

// Documents lists indexed documents with fragment/page/engine counts, newest
// first. Docs with no OCR-tracked pages (plain text) report Pages 0.
func (s *Store) Documents() ([]DocSummary, error) {
	rows, err := s.db.Query(
		`SELECT d.path, d.title, d.added_at,
		        (SELECT COUNT(*) FROM fragments f WHERE f.doc_id=d.id) AS frags
		 FROM documents d ORDER BY d.added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocSummary
	for rows.Next() {
		var ds DocSummary
		if err := rows.Scan(&ds.Path, &ds.Title, &ds.AddedAt, &ds.Fragments); err != nil {
			return nil, err
		}
		ds.Engines = map[string]int{}
		out = append(out, ds)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Per-doc engine breakdown (a second pass keeps the query simple).
	for i := range out {
		erows, err := s.db.Query(
			`SELECT p.engine, COUNT(*) FROM ocr_pages p
			 JOIN documents d ON d.id=p.doc_id
			 WHERE d.path=? GROUP BY p.engine`, out[i].Path)
		if err != nil {
			return nil, err
		}
		for erows.Next() {
			var eng string
			var n int
			if err := erows.Scan(&eng, &n); err != nil {
				erows.Close()
				return nil, err
			}
			out[i].Engines[eng] = n
			out[i].Pages += n
			if eng == "vision" {
				out[i].Vision += n
			}
		}
		erows.Close()
	}
	return out, nil
}

// PageReview is one page's OCR review: the engine that produced it, whether it
// needed the VLM, whether a page image is on disk, and the text indexed for it.
type PageReview struct {
	Page      int    `json:"page"`
	Engine    string `json:"engine"`
	Vision    bool   `json:"vision"`
	HasImage  bool   `json:"has_image"`
	Fragments int    `json:"fragments"`
	Text      string `json:"text"`
}

// DocReview returns a document's title and per-page OCR review (pages ≥ 1, in
// order). Page text is the concatenation of the fragments indexed for the page —
// i.e. what OCR/segmentation actually produced. Returns (‑, nil, nil) when the
// path is unknown.
func (s *Store) DocReview(path string) (title string, pages []PageReview, err error) {
	var docID int64
	err = s.db.QueryRow(`SELECT id, title FROM documents WHERE path=?`, path).Scan(&docID, &title)
	if err == sql.ErrNoRows {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.db.Query(
		`SELECT page, engine, image_path FROM ocr_pages WHERE doc_id=? ORDER BY page`, docID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pr PageReview
		var img string
		if err := rows.Scan(&pr.Page, &pr.Engine, &img); err != nil {
			return "", nil, err
		}
		pr.Vision = pr.Engine == "vision"
		pr.HasImage = img != ""
		pages = append(pages, pr)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	// Fill each page's indexed text from its fragments.
	for i := range pages {
		frows, err := s.db.Query(
			`SELECT text FROM fragments WHERE doc_id=? AND page=? ORDER BY ord`, docID, pages[i].Page)
		if err != nil {
			return "", nil, err
		}
		var parts []string
		for frows.Next() {
			var t string
			if err := frows.Scan(&t); err != nil {
				frows.Close()
				return "", nil, err
			}
			parts = append(parts, t)
		}
		frows.Close()
		pages[i].Fragments = len(parts)
		pages[i].Text = strings.Join(parts, "\n\n")
	}
	return title, pages, nil
}

// PageImagePath returns the absolute path of a document page's saved image, or
// "" if there is none. The daemon validates the path is under the home's pages/
// dir before serving it.
func (s *Store) PageImagePath(path string, page int) (string, error) {
	var img string
	err := s.db.QueryRow(
		`SELECT image_path FROM ocr_pages p JOIN documents d ON d.id=p.doc_id
		 WHERE d.path=? AND p.page=?`, path, page).Scan(&img)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return img, nil
}

// PagesRoot is the home's pages directory (absolute), for the daemon to bound
// page-image serving to the home. Empty for a no-home store.
func (s *Store) PagesRoot() string {
	if !s.withHome {
		return ""
	}
	if abs, err := filepath.Abs(s.home.PagesDir()); err == nil {
		return abs
	}
	return s.home.PagesDir()
}
