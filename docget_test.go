package raglit

import (
	"context"
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

	// max_chars caps the blob (pages stay whole).
	c, _ = s.DocText("file:///docs/invoice-2024.pdf", 0, 0, 10)
	if !c.Truncated || len(c.Text) != 10 {
		t.Fatalf("cap = %q trunc=%v", c.Text, c.Truncated)
	}
	if len(c.Pages) != 2 {
		t.Fatalf("cap should not drop pages: %d", len(c.Pages))
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
