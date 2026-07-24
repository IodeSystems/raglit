package raglit

import (
	"database/sql"
	"fmt"
	"strings"
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
func (s *Store) MatchDocuments(ref string) ([]DocRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	// Exact path first.
	var d DocRef
	err := s.db.QueryRow(`SELECT path, title FROM documents WHERE path=?`, ref).Scan(&d.Path, &d.Title)
	if err == nil {
		return []DocRef{d}, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	// Substring over path/title.
	like := "%" + strings.ToLower(ref) + "%"
	rows, err := s.db.Query(
		`SELECT path, title FROM documents
		 WHERE lower(path) LIKE ? OR lower(title) LIKE ?
		 ORDER BY added_at DESC`, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocRef
	for rows.Next() {
		var r DocRef
		if err := rows.Scan(&r.Path, &r.Title); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
func (s *Store) DocText(exactPath string, from, to, maxChars int) (DocContent, error) {
	var docID int64
	var out DocContent
	err := s.db.QueryRow(`SELECT id, path, title FROM documents WHERE path=?`, exactPath).
		Scan(&docID, &out.Path, &out.Title)
	if err == sql.ErrNoRows {
		return DocContent{}, fmt.Errorf("raglit: no document with path %q", exactPath)
	}
	if err != nil {
		return DocContent{}, err
	}

	q := `SELECT page, ord, text FROM fragments WHERE doc_id=?`
	args := []any{docID}
	if from > 0 {
		q += ` AND page>=?`
		args = append(args, from)
	}
	if to > 0 {
		q += ` AND page<=?`
		args = append(args, to)
	}
	q += ` ORDER BY page, ord`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return DocContent{}, err
	}
	defer rows.Close()

	// Group fragments into pages, preserving order.
	var curPage = -1
	var buf []string
	flush := func() {
		if curPage >= 0 {
			out.Pages = append(out.Pages, DocPageText{Page: curPage, Text: strings.Join(buf, "\n\n")})
		}
		buf = nil
	}
	for rows.Next() {
		var page, ord int
		var text string
		if err := rows.Scan(&page, &ord, &text); err != nil {
			return DocContent{}, err
		}
		if page != curPage {
			flush()
			curPage = page
		}
		buf = append(buf, text)
	}
	if err := rows.Err(); err != nil {
		return DocContent{}, err
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
