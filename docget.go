package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	gen "github.com/iodesystems/raglit/internal/db"
	"github.com/iodesystems/sqlc-go-codegen-metaquery/metaquery"
	"github.com/iodesystems/sqlc-go-codegen-metaquery/metaquery/mqsqlite"
)

// Document content retrieval — the read side for an agent that has a search hit
// (or a filename) and wants the actual indexed text back, not a snippet. Text is
// reassembled from the stored fragments in page/ord order, so it reflects what
// the index holds (post-OCR/segmentation), independent of the original file.

// DocRef identifies one indexed document (its stable path key + title).
type DocRef struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// MatchDocuments resolves a document reference to candidates: an exact path
// match wins (returns just that one); otherwise a case-insensitive substring
// match over path AND title. Empty ref returns nothing. The caller decides what
// to do with 0 / 1 / many (get_document treats >1 as ambiguous).
func (s *Store) matchDocumentsLocal(ref string) ([]DocRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	ctx := context.Background()
	// Exact path first.
	if d, err := s.q.GetDocumentByPath(ctx, ref); err == nil {
		return []DocRef{{Path: d.Path, Title: d.Title}}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Substring over path/title.
	like := "%" + strings.ToLower(ref) + "%"
	rows, err := s.q.MatchDocumentsLike(ctx, gen.MatchDocumentsLikeParams{Path: like, Title: like})
	if err != nil {
		return nil, err
	}
	out := make([]DocRef, len(rows))
	for i, r := range rows {
		out[i] = DocRef{Path: r.Path, Title: r.Title}
	}
	return out, nil
}

// DocPageText is one page's reassembled text.
type DocPageText struct {
	Page int    `json:"page"`
	Text string `json:"text"`
}

// DocContent is a document's indexed text: per-page plus a single joined blob.
type DocContent struct {
	Path      string        `json:"path"`
	Title     string        `json:"title"`
	Pages     []DocPageText `json:"pages"`
	Text      string        `json:"text"`
	Truncated bool          `json:"truncated"`
}

// DocText returns a document's indexed text, reassembled from its fragments in
// page/ord order. exactPath must be a stored document path (use MatchDocuments to
// resolve a filename first). from/to bound the page range inclusively (≤0 = open
// end); maxChars caps the joined Text blob (≤0 = uncapped), setting Truncated
// when it bites — the per-page array is left whole. Returns (‑, false, nil) via a
// zero DocContent when the path is unknown.
func (s *Store) docTextLocal(exactPath string, from, to, maxChars int) (DocContent, error) {
	ctx := context.Background()
	var out DocContent
	doc, err := s.q.GetDocumentByPath(ctx, exactPath)
	if errors.Is(err, sql.ErrNoRows) {
		return DocContent{}, fmt.Errorf("raglit: no document with path %q", exactPath)
	}
	if err != nil {
		return DocContent{}, err
	}
	out.Path, out.Title = doc.Path, doc.Title

	// Page-range filter via a metaquery Builder over the base ListFragmentsForDoc
	// (dynamic from/to WHERE + the page/ord ordering, no hand-built SQL).
	b := gen.WrapListFragmentsForDoc(doc.ID).OrderBy("page", metaquery.Asc).OrderBy("ord", metaquery.Asc)
	if from > 0 {
		b = b.Where("page", metaquery.OpGe, from)
	}
	if to > 0 {
		b = b.Where("page", metaquery.OpLe, to)
	}
	res, err := mqsqlite.Scan[gen.ListFragmentsForDocRow](ctx, s.db, b)
	if err != nil {
		return DocContent{}, err
	}

	// Group fragments into pages, preserving order.
	curPage := int64(-1)
	var buf []string
	flush := func() {
		if curPage >= 0 {
			out.Pages = append(out.Pages, DocPageText{Page: int(curPage), Text: strings.Join(buf, "\n\n")})
		}
		buf = nil
	}
	for _, r := range res.Data {
		if r.Page != curPage {
			flush()
			curPage = r.Page
		}
		buf = append(buf, r.Text)
	}
	flush()

	parts := make([]string, len(out.Pages))
	for i, p := range out.Pages {
		parts[i] = p.Text
	}
	out.Text = strings.Join(parts, "\n\n")
	if maxChars > 0 && len(out.Text) > maxChars {
		out.Text = out.Text[:maxChars]
		out.Truncated = true
	}
	return out, nil
}
