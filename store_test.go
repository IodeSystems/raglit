package raglit

import (
	"context"
	"testing"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIngestAndSearch_BM25Ranks(t *testing.T) {
	s := openMem(t)
	must(t, s.Ingest(Document{Path: "auth.md", Title: "Auth", Fragments: []Fragment{
		{Page: 1, Ord: 0, Text: "Access tokens expire; the refresh token rotates on each use."},
		{Page: 1, Ord: 1, Text: "Unrelated note about invoicing and billing."},
	}}))
	must(t, s.Ingest(Document{Path: "deploy.md", Title: "Deploy", Fragments: []Fragment{
		{Page: 1, Ord: 0, Text: "Blue-green deploy; rollback flips the load balancer."},
	}}))

	hits, err := s.Search("refresh token expire", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected matches")
	}
	// The auth-refresh fragment must rank first (most query terms).
	if hits[0].Path != "auth.md" || hits[0].Page != 1 || hits[0].Ord != 0 {
		t.Fatalf("wrong top hit: %+v", hits[0])
	}
	if hits[0].Score <= 0 {
		t.Errorf("Score should be positive (higher=better), got %v", hits[0].Score)
	}
}

func TestReingestIsIdempotent(t *testing.T) {
	s := openMem(t)
	doc := Document{Path: "a.md", Fragments: []Fragment{{Text: "hello world foo"}}}
	must(t, s.Ingest(doc))
	must(t, s.Ingest(doc)) // same path again — must replace, not duplicate
	hits, _ := s.Search("hello", 10)
	if len(hits) != 1 {
		t.Fatalf("reingest duplicated fragments: got %d hits, want 1", len(hits))
	}
	// Changed content converges (old fragment gone).
	must(t, s.Ingest(Document{Path: "a.md", Fragments: []Fragment{{Text: "totally different"}}}))
	if h, _ := s.Search("hello", 10); len(h) != 0 {
		t.Fatalf("stale fragment survived reingest: %+v", h)
	}
	if h, _ := s.Search("different", 10); len(h) != 1 {
		t.Fatalf("new fragment not indexed: %d", len(h))
	}
}

func TestSearch_ToleratesPunctuationAndOperators(t *testing.T) {
	s := openMem(t)
	must(t, s.Ingest(Document{Path: "x.md", Fragments: []Fragment{{Text: "what's the plan for a-b testing"}}}))
	// Raw FTS5 would choke on the apostrophe / hyphen / bare OR; ftsQuery quotes.
	for _, q := range []string{"what's", "a-b testing", "plan OR nothing"} {
		if _, err := s.Search(q, 5); err != nil {
			t.Errorf("query %q errored: %v", q, err)
		}
	}
}

func TestFinder_CollapsesToBestPerDoc(t *testing.T) {
	s := openMem(t)
	// Two fragments in ONE doc both match — Finder must emit ONE DocHit.
	must(t, s.Ingest(Document{Path: "auth.md", Title: "Auth", Fragments: []Fragment{
		{Page: 1, Ord: 0, Text: "token refresh flow"},
		{Page: 2, Ord: 0, Text: "token expiry and refresh again"},
	}}))
	f := NewFinder(s)
	hits, err := f.Find(context.Background(), []string{"token refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 DocHit per document, got %d", len(hits))
	}
	if hits[0].DocID != "auth.md" || hits[0].Title != "Auth" || hits[0].Line == "" {
		t.Errorf("bad DocHit: %+v", hits[0])
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
