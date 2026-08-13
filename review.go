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
	Pages     int            `json:"pages"`     // OCR-tracked pages (page ≥ 1)
	Vision    int            `json:"vision"`    // pages that used the VLM
	FragMode  string         `json:"frag_mode"` // how it was fragmented: text-overlap | llm-seg
	Engines   map[string]int `json:"engines"`   // engine → page count
	AddedAt   int64          `json:"added_at"`
	// GenName / GenKind / GenSource are what the document IS (identity.go), for a
	// list a person can navigate. Both names travel: the caption is what a reader
	// needs, and the filename is what everything else joins on — and where they
	// disagree, THAT is the finding.
	GenName   string `json:"gen_name,omitempty"`
	GenKind   string `json:"gen_kind,omitempty"`
	GenSource string `json:"gen_source,omitempty"` // machine | person
}

// Documents lists indexed documents with fragment/page/engine counts, newest
// first. Docs with no OCR-tracked pages (plain text) report Pages 0.
// Raw SQL rather than the generated ListDocumentSummaries, for two columns it
// does not have and one count it gets wrong: the identity fragment is a caption
// ABOUT the document, so it belongs neither in the document's fragment count nor
// invisible in its row. (Raw for the reason TruePages gives — see its comment on
// regenerating the sqlc layer.)
//
// The exclusion is `origin <> 'identity'`, NOT `origin = ''`, and the difference
// is load-bearing. A photograph's only fragment is the model's DESCRIPTION of it
// (origin='described'), which is not a caption about the document — it IS the
// document's indexed content, and all of it there will ever be. Written the
// other way, every photograph in the corpus reported zero fragments while search
// returned it perfectly well: indexed, searchable, and listed as if it were the
// no-fragments failure. Measured on the delano photo sets, 17 of them.
func (s *Store) documentsLocal() ([]DocSummary, error) {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.path, d.title, d.added_at, d.frag_mode, d.gen_name, d.gen_kind, d.gen_source,
		        (SELECT COUNT(*) FROM fragments f WHERE f.doc_id = d.id AND f.origin <> 'identity') AS fragments
		   FROM documents d ORDER BY d.added_at DESC`)
	if err != nil {
		return nil, err
	}
	// Drained FULLY before the per-document queries below. An open rows cursor
	// holds its connection, and the pool answers the next query on a second one —
	// which for a ":memory:" index is a DIFFERENT, empty database. That is not a
	// slow path, it is "no such table: ocr_pages".
	var out []DocSummary
	var ids []int64
	for rows.Next() {
		var id, addedAt, frags int64
		var path, title, fragMode, genName, genKind, genSource string
		if err := rows.Scan(&id, &path, &title, &addedAt, &fragMode, &genName, &genKind, &genSource, &frags); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, DocSummary{Path: path, Title: title, Fragments: int(frags), FragMode: fragMode,
			AddedAt: addedAt, Engines: map[string]int{},
			GenName: genName, GenKind: genKind, GenSource: genSource})
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i, id := range ids {
		// Per-doc engine breakdown (a second pass keeps the query simple).
		ec, err := s.q.OcrEngineCountsByDoc(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, e := range ec {
			n := int(e.N)
			out[i].Engines[e.Engine] = n
			out[i].Pages += n
			if e.Engine == "vision" {
				out[i].Vision += n
			}
		}
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
