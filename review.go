package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gen "github.com/iodesystems/raglit/internal/db"
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
	return s.q.UpsertOcrPage(context.Background(), gen.UpsertOcrPageParams{
		DocID: docID, Page: int64(page), Engine: engine, ImagePath: imagePath,
	})
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
	ctx := context.Background()
	rows, err := s.q.ListDocumentSummaries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DocSummary, len(rows))
	for i, r := range rows {
		ds := DocSummary{Path: r.Path, Title: r.Title, Fragments: int(r.Fragments), AddedAt: r.AddedAt, Engines: map[string]int{}}
		// Per-doc engine breakdown (a second pass keeps the query simple).
		ec, err := s.q.OcrEngineCountsByDoc(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range ec {
			n := int(e.N)
			ds.Engines[e.Engine] = n
			ds.Pages += n
			if e.Engine == "vision" {
				ds.Vision += n
			}
		}
		out[i] = ds
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
	ctx := context.Background()
	doc, err := s.q.GetDocumentByPath(ctx, path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	title = doc.Title
	prows, err := s.q.ListOcrPagesByDoc(ctx, doc.ID)
	if err != nil {
		return "", nil, err
	}
	for _, pr := range prows {
		page := PageReview{
			Page: int(pr.Page), Engine: pr.Engine,
			Vision: pr.Engine == "vision", HasImage: pr.ImagePath != "",
		}
		// Page text is the concatenation of the fragments indexed for the page.
		texts, err := s.q.ListFragmentTextByPage(ctx, gen.ListFragmentTextByPageParams{DocID: doc.ID, Page: pr.Page})
		if err != nil {
			return "", nil, err
		}
		page.Fragments = len(texts)
		page.Text = strings.Join(texts, "\n\n")
		pages = append(pages, page)
	}
	return title, pages, nil
}

// PageImagePath returns the absolute path of a document page's saved image, or
// "" if there is none. The daemon validates the path is under the home's pages/
// dir before serving it.
func (s *Store) PageImagePath(path string, page int) (string, error) {
	img, err := s.q.GetPageImagePath(context.Background(), gen.GetPageImagePathParams{Path: path, Page: int64(page)})
	if errors.Is(err, sql.ErrNoRows) {
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
