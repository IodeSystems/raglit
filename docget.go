package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	gen "github.com/iodesystems/raglit/internal/db"
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

	// Raw SQL rather than a metaquery Builder over ListFragmentsForDoc, because
	// of the origin filter: this function's contract is "the document's indexed
	// text", and the generated identity fragment is a model's DESCRIPTION of the
	// document. A Builder cannot express it — it wraps the base query as a
	// derived table, whose columns are the five the query selects, so filtering on
	// origin there fails with "no such column". (Raw for the reason TruePages
	// gives; see its comment on regenerating the sqlc layer.)
	q := `SELECT page, ord, text, start_off, end_off FROM fragments
	       WHERE doc_id = ? AND ` + SQLOwnWords
	args := []any{doc.ID}
	if from > 0 {
		q += " AND page >= ?"
		args = append(args, from)
	}
	if to > 0 {
		q += " AND page <= ?"
		args = append(args, to)
	}
	q += " ORDER BY page, ord"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return DocContent{}, err
	}
	defer rows.Close()
	var frags []gen.ListFragmentsForDocRow
	for rows.Next() {
		var r gen.ListFragmentsForDocRow
		if err := rows.Scan(&r.Page, &r.Ord, &r.Text, &r.StartOff, &r.EndOff); err != nil {
			return DocContent{}, err
		}
		frags = append(frags, r)
	}
	if err := rows.Err(); err != nil {
		return DocContent{}, err
	}

	// text-overlap documents have overlapping fragments (a plain join would repeat
	// every overlap region), so reassemble from their [start,end) spans; llm-seg /
	// synthetic documents have no spans (0/0) and join directly.
	offsetMode := false
	for _, r := range frags {
		if r.EndOff > r.StartOff {
			offsetMode = true
			break
		}
	}
	if offsetMode {
		out.Pages, out.Text = reassembleOffsets(frags)
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
		for _, r := range frags {
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

// TruePages reassembles a document at TRUE page grain, honouring page_spans.
//
// DocText's Pages array is not this, and the difference is the reason this
// exists. The assembler absorbs a page's continuation (or a sub-floor sibling)
// from the NEXT page into an open fragment and keeps only the START page in
// `fragments.page`, so DocText attributes every byte of a multi-page fragment to
// the page the fragment opened on. Measured on this index: 67 of 460 fragments
// carry page boundaries, and honouring them recovers 441 true pages where grouping
// by `fragments.page` finds only 277. Halvor's declaration is the clearest case —
// 14 fragments opening on pages 1, 4, 14, 17, 18, 23, 24, 28 and 33, so DocText
// reports it as a 9-page document and every page number after the first stitch is
// wrong. That is exactly wrong for anything that wants to SAY where a finding is.
//
// page_spans was added to fix that ("a search hit inside one could not be
// resolved to the page it is actually on", per the schema) and, until this, was
// written by ingest, carried through the pool, and read by nothing but its own
// tests. Similarity is its first consumer: "pages 12-14 of X are pages 1-3 of Y"
// is the whole deliverable, and a page number that is silently the fragment's
// start page makes that report a lie.
//
// Pages come back in page order, contiguous from the lowest page present, with
// empty pages included — a consumer resolving an alignment to a page depends on
// the numbering being complete, the same contract the transcription markdown
// makes.
// Raw SQL, not sqlc, and not by preference. The generated
// ListFragmentsForDoc does not select page_spans, and regenerating is currently
// destructive: `sqlc generate` with the installed toolchain (sqlc v1.30.0 against
// the metaquery plugin built on plugin-sdk-go v1.23.0) rewrites the SQL text of
// EVERY query in internal/db — each one comes back rotated two characters, e.g.
//
//	const cancelJob = `-- name: CancelJob :execrows
//	);
//
//	DELETE FROM ingest_jobs WHERE id=? AND state='pendin`
//
// That is 60+ silently broken queries for the sake of one added column, so this
// follows the precedent the schema file already sets for FTS5 and the vector scan:
// what sqlc cannot express here stays raw. Fix the plugin, then fold this back in.
func (s *Store) TruePages(exactPath string) ([]PageText, error) {
	ctx := context.Background()
	doc, err := s.q.GetDocumentByPath(ctx, exactPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("raglit: no document with path %q", exactPath)
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT page, ord, text, start_off, end_off, page_spans
		 FROM fragments WHERE doc_id = ? AND `+SQLOwnWords+` ORDER BY page, ord`, doc.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var frags []fragRow
	for rows.Next() {
		var f fragRow
		if err := rows.Scan(&f.Page, &f.Ord, &f.Text, &f.StartOff, &f.EndOff, &f.PageSpans); err != nil {
			return nil, err
		}
		frags = append(frags, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return truePages(frags), nil
}

// fragRow is one fragment as TruePages reads it. Mirrors what sqlc would have
// generated for ListFragmentsForDoc had page_spans been in the select list.
type fragRow struct {
	Page      int64
	Ord       int64
	Text      string
	StartOff  int64
	EndOff    int64
	PageSpans string
}

// truePages is TruePages' pure core, so the span arithmetic is testable without
// a database.
//
// Two things happen at once and both are needed. Overlapping windows are
// de-overlapped by [start,end) high-water mark (as reassembleOffsets does), and
// the surviving bytes of each fragment are split across pages by that fragment's
// page_spans. Doing only the first attributes a stitched page to the wrong page;
// doing only the second repeats every overlap region.
//
// A fragment whose spans are absent (llm-seg, or a document indexed before the
// column existed) contributes wholly to its start page, which is what the old
// behaviour already assumed — so this degrades to DocText's answer rather than
// failing. Note that a page_spans-carrying fragment may ALSO have no offsets:
// llm-seg fragments span pages too, and 67 of the 67 span-carrying fragments on
// this index are offset-less, so the two conditions are independent and were
// briefly conflated here (which dropped every stitched llm-seg page).
func truePages(rows []fragRow) []PageText {
	offsetMode := false
	for _, r := range rows {
		if r.EndOff > r.StartOff {
			offsetMode = true
			break
		}
	}
	if offsetMode {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].StartOff < rows[j].StartOff })
	}
	byPage := map[int]*strings.Builder{}
	lo, hi := 0, 0
	have := false
	put := func(page int, text string) {
		if text == "" {
			// Still register the page, so a blank page keeps its slot.
			if _, ok := byPage[page]; !ok {
				byPage[page] = &strings.Builder{}
			}
		} else {
			bb, ok := byPage[page]
			if !ok {
				bb = &strings.Builder{}
				byPage[page] = bb
			}
			if bb.Len() > 0 {
				bb.WriteString(pageSep)
			}
			bb.WriteString(text)
		}
		if !have || page < lo {
			lo = page
		}
		if !have || page > hi {
			hi = page
		}
		have = true
	}

	covered := int64(0)
	for _, r := range rows {
		text := r.Text
		skip := 0
		if offsetMode && r.EndOff > r.StartOff {
			if r.EndOff <= covered {
				continue // fully covered by an earlier fragment
			}
			if covered > r.StartOff {
				skip = int(covered - r.StartOff)
			}
			if skip < 0 || skip > len(text) {
				continue
			}
			covered = r.EndOff
		}
		spans := DecodePageSpans(r.PageSpans)
		if len(spans) < 2 {
			put(int(r.Page), text[skip:])
			continue
		}
		// Cut the surviving suffix at each page boundary at or after `skip`. The
		// page of the first surviving byte comes from PageAt, not from spans[0]:
		// an overlap can consume the whole of the fragment's first page.
		start := skip
		page := PageAt(int(r.Page), spans, skip)
		for _, sp := range spans {
			if sp.Off <= start || sp.Off > len(text) {
				continue
			}
			put(page, text[start:sp.Off])
			start, page = sp.Off, sp.Page
		}
		put(page, text[start:])
	}

	if !have {
		return nil
	}
	out := make([]PageText, 0, hi-lo+1)
	for p := lo; p <= hi; p++ {
		var t string
		if bb, ok := byPage[p]; ok {
			t = bb.String()
		}
		out = append(out, PageText{Page: p, Text: t, Engine: "text"})
	}
	return out
}
