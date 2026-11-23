package raglit

import (
	"context"
	"strings"
	"testing"
)

func seedDocs(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "file:///docs/invoice-2024.pdf", Title: "Invoice 2024", Fragments: []Fragment{
		{Page: 1, Ord: 0, Text: "Invoice header"},
		{Page: 1, Ord: 1, Text: "Line items"},
		{Page: 2, Ord: 0, Text: "Total due"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest(ctx, Document{Path: "file:///docs/notes.md", Title: "Notes", Fragments: []Fragment{
		{Page: 0, Ord: 0, Text: "alpha"},
		{Page: 0, Ord: 1, Text: "bravo"},
	}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMatchDocuments(t *testing.T) {
	s := seedDocs(t)
	defer s.Close()

	// Exact path wins → single.
	got, err := s.MatchDocuments("file:///docs/notes.md")
	if err != nil || len(got) != 1 || got[0].Title != "Notes" {
		t.Fatalf("exact match = %v, %v", got, err)
	}
	// Substring over path.
	got, _ = s.MatchDocuments("invoice")
	if len(got) != 1 || got[0].Path != "file:///docs/invoice-2024.pdf" {
		t.Fatalf("substring path = %v", got)
	}
	// Substring over title (case-insensitive).
	got, _ = s.MatchDocuments("NOTES")
	if len(got) != 1 || got[0].Path != "file:///docs/notes.md" {
		t.Fatalf("substring title = %v", got)
	}
	// Broad substring → many.
	got, _ = s.MatchDocuments("docs")
	if len(got) != 2 {
		t.Fatalf("broad substring = %d docs, want 2", len(got))
	}
	// No match.
	if got, _ := s.MatchDocuments("nonexistent"); len(got) != 0 {
		t.Fatalf("no match = %v", got)
	}
	// Empty ref.
	if got, _ := s.MatchDocuments("  "); got != nil {
		t.Fatalf("empty ref = %v", got)
	}
}

func TestDocText_FullAndRange(t *testing.T) {
	s := seedDocs(t)
	defer s.Close()

	// Whole document: pages grouped, joined blob.
	c, err := s.DocText("file:///docs/invoice-2024.pdf", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Invoice 2024" || len(c.Pages) != 2 {
		t.Fatalf("content = %+v", c)
	}
	if c.Pages[0].Page != 1 || c.Pages[0].Text != "Invoice header\n\nLine items" {
		t.Fatalf("page1 = %+v", c.Pages[0])
	}
	if c.Pages[1].Page != 2 || c.Pages[1].Text != "Total due" {
		t.Fatalf("page2 = %+v", c.Pages[1])
	}
	if c.Text != "Invoice header\n\nLine items\n\nTotal due" || c.Truncated {
		t.Fatalf("joined text = %q trunc=%v", c.Text, c.Truncated)
	}

	// Single page via range.
	c, _ = s.DocText("file:///docs/invoice-2024.pdf", 2, 2, 0)
	if len(c.Pages) != 1 || c.Pages[0].Page != 2 || c.Text != "Total due" {
		t.Fatalf("range p2 = %+v", c)
	}

	// max_chars caps the whole response: the blob AND the page array, cut at the
	// same offset. Doc is "Invoice header\n\nLine items" (p1) + "Total due" (p2).
	c, _ = s.DocText("file:///docs/invoice-2024.pdf", 0, 0, 10)
	if !c.Truncated || c.Text != "Invoice he" {
		t.Fatalf("cap = %q trunc=%v", c.Text, c.Truncated)
	}
	if len(c.Pages) != 1 || c.Pages[0].Text != c.Text {
		t.Fatalf("cap should cut pages to the blob: %+v", c.Pages)
	}

	// A cap landing inside a later page keeps the earlier pages whole.
	c, _ = s.DocText("file:///docs/invoice-2024.pdf", 0, 0, 32)
	if len(c.Pages) != 2 || c.Pages[0].Text != "Invoice header\n\nLine items" || c.Pages[1].Text != "Tota" {
		t.Fatalf("straddling cap = %+v", c.Pages)
	}
	if joinPages(c.Pages) != c.Text {
		t.Fatalf("pages %q != text %q", joinPages(c.Pages), c.Text)
	}

	// A cap at/above the full length leaves everything whole and untruncated.
	c, _ = s.DocText("file:///docs/invoice-2024.pdf", 0, 0, 1000)
	if c.Truncated || len(c.Pages) != 2 || c.Text != "Invoice header\n\nLine items\n\nTotal due" {
		t.Fatalf("no-op cap = %+v", c)
	}

	// Page-0 (plain text) doc.
	c, _ = s.DocText("file:///docs/notes.md", 0, 0, 0)
	if len(c.Pages) != 1 || c.Pages[0].Page != 0 || c.Text != "alpha\n\nbravo" {
		t.Fatalf("notes = %+v", c)
	}

	// Unknown path errors.
	if _, err := s.DocText("file:///nope", 0, 0, 0); err == nil {
		t.Fatal("unknown path should error")
	}
}

func joinPages(pages []DocPageText) string {
	parts := make([]string, len(pages))
	for i, p := range pages {
		parts[i] = p.Text
	}
	return strings.Join(parts, "\n\n")
}

// TestDocTextOpts_Offset pages into a long single-page document at a character
// offset, bounded by maxChars. The joined text for the seeded invoice doc is
// "Invoice header\n\nLine items\n\nTotal due" (40 chars).
func TestDocTextOpts_Offset(t *testing.T) {
	s := seedDocs(t)
	defer s.Close()

	// Offset 0 is the same as no offset.
	c, err := s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "Invoice header\n\nLine items\n\nTotal due" {
		t.Fatalf("offset 0 = %q", c.Text)
	}

	// Offset 15 starts mid-"Line items": "e items\n\nTotal due".
	c, _ = s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Offset: 15})
	want := "Invoice header\n\nLine items\n\nTotal due"[15:]
	if c.Text != want {
		t.Fatalf("offset 15 = %q, want %q", c.Text, want)
	}

	// Offset + maxChars: offset 10, cap 8 → "header\n\n"[:8] = "header\n\n"
	// Actually "Invoice header\n\nLine items\n\nTotal due"[10:] = "ader\n\nLine items\n\nTotal due", cap 8 → "ader\n\nLi"
	c, _ = s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 8, DocContentOpts{Offset: 10})
	full := "Invoice header\n\nLine items\n\nTotal due"
	if !c.Truncated || c.Text != full[10:18] {
		t.Fatalf("offset 10 cap 8 = %q trunc=%v, want %q", c.Text, c.Truncated, full[10:18])
	}

	// Offset past the end returns empty text, not an error.
	c, _ = s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Offset: 999})
	if c.Text != "" {
		t.Fatalf("offset past end = %q, want empty", c.Text)
	}

	// Query is nil when no query is given.
	if c, _ := s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Offset: 5}); c.Match != nil {
		t.Fatalf("no query should give nil Match, got %+v", c.Match)
	}
}

// TestDocTextOpts_Query returns only the fragments matching a case-insensitive
// substring, in document order, via the Match field.
func TestDocTextOpts_Query(t *testing.T) {
	s := seedDocs(t)
	defer s.Close()

	// "invoice" matches only the first fragment (page 1, "Invoice header").
	c, err := s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Query: "invoice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Match) != 1 || c.Match[0].Page != 1 || c.Match[0].Text != "Invoice header" {
		t.Fatalf("query invoice = %+v", c.Match)
	}
	// Text and Pages carry only the matched fragments.
	if c.Text != "Invoice header" {
		t.Fatalf("query text = %q, want %q", c.Text, "Invoice header")
	}
	if len(c.Pages) != 1 || c.Pages[0].Page != 1 || c.Pages[0].Text != "Invoice header" {
		t.Fatalf("query pages = %+v", c.Pages)
	}

	// Case-insensitive: "INVOICE" matches the same fragment.
	c, _ = s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Query: "INVOICE"})
	if len(c.Match) != 1 || c.Match[0].Text != "Invoice header" {
		t.Fatalf("query INVOICE = %+v", c.Match)
	}

	// "line" matches the second fragment (page 1, "Line items").
	c, _ = s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Query: "line"})
	if len(c.Match) != 1 || c.Match[0].Page != 1 || c.Match[0].Text != "Line items" {
		t.Fatalf("query line = %+v", c.Match)
	}

	// A term in two fragments returns both, in document order.
	// Seed a doc with "total" on two pages.
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "file:///docs/summary.pdf", Title: "Summary", Fragments: []Fragment{
		{Page: 1, Ord: 0, Text: "total revenue"},
		{Page: 2, Ord: 0, Text: "total cost"},
		{Page: 3, Ord: 0, Text: "net profit"},
	}}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.DocTextOpts("file:///docs/summary.pdf", 0, 0, 0, DocContentOpts{Query: "total"})
	if len(c.Match) != 2 {
		t.Fatalf("query total = %d matches, want 2: %+v", len(c.Match), c.Match)
	}
	if c.Match[0].Page != 1 || c.Match[0].Text != "total revenue" {
		t.Fatalf("match[0] = %+v", c.Match[0])
	}
	if c.Match[1].Page != 2 || c.Match[1].Text != "total cost" {
		t.Fatalf("match[1] = %+v", c.Match[1])
	}
	// Text is the joined matched fragments.
	if c.Text != "total revenue\n\ntotal cost" {
		t.Fatalf("query text = %q", c.Text)
	}

	// No match returns empty Match, empty Text.
	c, _ = s.DocTextOpts("file:///docs/invoice-2024.pdf", 0, 0, 0, DocContentOpts{Query: "zebra"})
	if len(c.Match) != 0 || c.Text != "" {
		t.Fatalf("query zebra = match=%+v text=%q, want empty", c.Match, c.Text)
	}
}

// TestDocTextOpts_NoParamsUnchanged proves that omitting both offset and query
// returns exactly what DocText returns today: same text, pages, truncation.
func TestDocTextOpts_NoParamsUnchanged(t *testing.T) {
	s := seedDocs(t)
	defer s.Close()

	path := "file:///docs/invoice-2024.pdf"

	// No opts at all (zero value) must equal DocText.
	base, err := s.DocText(path, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	opt, _ := s.DocTextOpts(path, 0, 0, 0, DocContentOpts{})
	if opt.Text != base.Text || len(opt.Pages) != len(base.Pages) || opt.Truncated != base.Truncated {
		t.Fatalf("no-opts differs: text=%q pages=%d trunc=%v vs base text=%q pages=%d trunc=%v",
			opt.Text, len(opt.Pages), opt.Truncated, base.Text, len(base.Pages), base.Truncated)
	}
	if opt.Match != nil {
		t.Fatalf("no-opts should not populate Match")
	}

	// Page range + maxChars still work with zero opts.
	base, _ = s.DocText(path, 2, 2, 0)
	opt, _ = s.DocTextOpts(path, 2, 2, 0, DocContentOpts{})
	if opt.Text != base.Text || len(opt.Pages) != len(base.Pages) {
		t.Fatalf("range+no-opts differs: %+v vs %+v", opt, base)
	}

	base, _ = s.DocText(path, 0, 0, 10)
	opt, _ = s.DocTextOpts(path, 0, 0, 10, DocContentOpts{})
	if opt.Text != base.Text || opt.Truncated != base.Truncated {
		t.Fatalf("cap+no-opts differs: %q/%v vs %q/%v", opt.Text, opt.Truncated, base.Text, base.Truncated)
	}
}
