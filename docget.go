package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
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

// DocContent is a document's indexed text: per-page plus a single joined blob,
// plus any figures explained into it.
type DocContent struct {
	Path      string        `json:"path"`
	Title     string        `json:"title"`
	Pages     []DocPageText `json:"pages"`
	Text      string        `json:"text"`
	Truncated bool          `json:"truncated"`
	Figures   []FigureRef   `json:"figures,omitempty"`
}

// FigureRef is one figure attached to a document (or a search hit): its
// description and image, located by page.
type FigureRef struct {
	Page        int    `json:"page"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	ImagePath   string `json:"image_path,omitempty"`
	Bbox        string `json:"bbox,omitempty"`
}

// pageSep joins fragments into a page and pages into the Text blob.
const pageSep = "\n\n"

// DocText returns a document's indexed text, reassembled from its fragments in
// page/ord order. exactPath must be a stored document path (use MatchDocuments to
// resolve a filename first). from/to bound the page range inclusively (≤0 = open
// end); maxChars caps the WHOLE result (≤0 = uncapped) — both the joined Text
// blob and the Pages array, cut at the same offsets — setting Truncated when it
// bites. Returns (‑, false, nil) via a zero DocContent when the path is unknown.
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

	// text-overlap documents have overlapping fragments (a plain join would repeat
	// every overlap region), so reassemble from their [start,end) spans; llm-seg /
	// synthetic documents have no spans (0/0) and join directly.
	offsetMode := false
	for _, r := range res.Data {
		if r.EndOff > r.StartOff {
			offsetMode = true
			break
		}
	}
	if offsetMode {
		out.Pages, out.Text = reassembleOffsets(res.Data)
	} else {
		// Group fragments into pages, preserving order; join with pageSep.
		curPage := int64(-1)
		var buf []string
		flush := func() {
			if curPage >= 0 {
				out.Pages = append(out.Pages, DocPageText{Page: int(curPage), Text: strings.Join(buf, pageSep)})
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
		out.Text = strings.Join(parts, pageSep)
	}
	if maxChars > 0 && len(out.Text) > maxChars {
		out.Text = out.Text[:maxChars]
		out.Truncated = true
		out.Pages = capPages(out.Pages, maxChars)
	}
	// Figures explained into this document (within the requested page range).
	if mrows, err := s.q.ListMediaByDoc(ctx, doc.ID); err == nil {
		for _, m := range mrows {
			p := int(m.Page)
			if (from > 0 && p < from) || (to > 0 && p > to) {
				continue
			}
			out.Figures = append(out.Figures, FigureRef{
				Page: p, Kind: m.Kind, Description: m.Description, ImagePath: m.ImagePath, Bbox: m.Bbox,
			})
		}
	}
	return out, nil
}

// reassembleOffsets reconstructs an overlapping-window document exactly ONCE from
// its fragments' [start,end) source spans: fragments are walked in offset order,
// and only the bytes past the running high-water mark are appended, so shared
// overlap regions are not repeated. Each run of bytes is attributed to its
// fragment's page (the page it started on). Returns the per-page texts and the
// full de-overlapped blob (which equals the original source text).
func reassembleOffsets(rows []gen.ListFragmentsForDocRow) ([]DocPageText, string) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].StartOff < rows[j].StartOff })
	var full strings.Builder
	var pages []DocPageText
	var pageBuf strings.Builder
	curPage := int64(-1)
	flush := func() {
		if curPage >= 0 {
			pages = append(pages, DocPageText{Page: int(curPage), Text: pageBuf.String()})
		}
		pageBuf.Reset()
	}
	covered := int64(0) // highest source offset written so far
	for _, r := range rows {
		if r.EndOff <= covered {
			continue // fully covered by an earlier fragment
		}
		off := int64(0)
		if covered > r.StartOff {
			off = covered - r.StartOff // skip the shared prefix
		}
		if off < 0 || int(off) > len(r.Text) {
			continue
		}
		seg := r.Text[off:]
		if r.Page != curPage {
			flush()
			curPage = r.Page
		}
		pageBuf.WriteString(seg)
		full.WriteString(seg)
		covered = r.EndOff
	}
	flush()
	return pages, full.String()
}

// capPages cuts a page array to the same maxChars budget as the joined Text
// blob, at the same offsets: pages that fit stay whole, the page straddling the
// cap is truncated, later pages are dropped. Without this the cap bounded only
// Text while Pages carried the whole document — useless to a caller using
// max_chars to bound how much it takes back.
func capPages(pages []DocPageText, maxChars int) []DocPageText {
	used := 0
	for i, p := range pages {
		if i > 0 {
			used += len(pageSep) // the join preceding this page
		}
		if used >= maxChars {
			return pages[:i]
		}
		if room := maxChars - used; len(p.Text) > room {
			pages[i].Text = p.Text[:room]
			return pages[:i+1]
		}
		used += len(p.Text)
	}
	return pages
}
