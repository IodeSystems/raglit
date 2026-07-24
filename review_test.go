package raglit

import (
	"context"
	"testing"
)

func TestJobControl_RetryCancelList(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	bad, _ := s.Enqueue("file:///bad.pdf", "")
	good, _ := s.Enqueue("file:///good.txt", "")
	pend, _ := s.Enqueue("file:///later.txt", "")

	if err := s.failJob(bad, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.completeJob(good, 3, "offline"); err != nil {
		t.Fatal(err)
	}

	// Retry an errored job → back to pending, error cleared.
	if err := s.RetryJob(bad); err != nil {
		t.Fatalf("retry error job: %v", err)
	}
	// Retrying a pending job is not allowed.
	if err := s.RetryJob(bad); err == nil {
		t.Fatal("retrying a pending job should error")
	}
	// Cancel a pending job → removed.
	if err := s.CancelJob(pend); err != nil {
		t.Fatalf("cancel pending: %v", err)
	}
	if err := s.CancelJob(good); err == nil {
		t.Fatal("canceling a done job should error")
	}

	jobs, err := s.Jobs("all", 100)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]JobInfo{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	if _, ok := byID[pend]; ok {
		t.Fatal("canceled job should be gone from the list")
	}
	if byID[bad].State != "pending" || byID[bad].Error != "" {
		t.Fatalf("retried job = %+v, want pending w/ no error", byID[bad])
	}
	if got := len(jobs); got != 2 {
		t.Fatalf("jobs listed = %d, want 2 (bad+good)", got)
	}

	pendOnly, _ := s.Jobs("pending", 100)
	if len(pendOnly) != 1 || pendOnly[0].ID != bad {
		t.Fatalf("pending filter = %v, want [bad]", pendOnly)
	}
}

func TestDocReview_PagesFromProvenance(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// A doc with fragments on two pages.
	if err := s.Ingest(ctx, Document{Path: "file:///scan.pdf", Title: "Scan", Fragments: []Fragment{
		{Page: 1, Ord: 0, Text: "page one alpha"},
		{Page: 2, Ord: 0, Text: "page two beta"},
		{Page: 2, Ord: 1, Text: "page two gamma"},
	}}); err != nil {
		t.Fatal(err)
	}
	var docID int64
	if err := s.db.QueryRow(`SELECT id FROM documents WHERE path=?`, "file:///scan.pdf").Scan(&docID); err != nil {
		t.Fatal(err)
	}
	// Record provenance: p1 born-digital text, p2 VLM-OCR'd with an image.
	if err := s.recordPage(docID, 1, "text", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.recordPage(docID, 2, "vision", "/some/pages/scan/p0002.png"); err != nil {
		t.Fatal(err)
	}

	title, pages, err := s.DocReview("file:///scan.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if title != "Scan" {
		t.Fatalf("title = %q", title)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	if pages[0].Engine != "text" || pages[0].Vision || pages[0].HasImage {
		t.Fatalf("p1 = %+v, want text/no-vision/no-image", pages[0])
	}
	if pages[1].Engine != "vision" || !pages[1].Vision || !pages[1].HasImage {
		t.Fatalf("p2 = %+v, want vision/has-image", pages[1])
	}
	if pages[1].Fragments != 2 || pages[1].Text != "page two beta\n\npage two gamma" {
		t.Fatalf("p2 text = %q (frags %d)", pages[1].Text, pages[1].Fragments)
	}

	// Documents summary reflects the engine breakdown.
	docs, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	d := docs[0]
	if d.Pages != 2 || d.Vision != 1 || d.Fragments != 3 {
		t.Fatalf("doc summary = %+v, want pages2/vision1/frags3", d)
	}
	if d.Engines["text"] != 1 || d.Engines["vision"] != 1 {
		t.Fatalf("engines = %v", d.Engines)
	}

	// Unknown doc → empty, no error.
	if _, pgs, err := s.DocReview("file:///nope"); err != nil || pgs != nil {
		t.Fatalf("unknown doc = (%v,%v), want (nil,nil)", pgs, err)
	}
}

func TestCommitDoc_ReplacesPriorPages(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First commit: one vision page.
	if err := s.commitDoc("file:///d", "D",
		[]stagedFrag{{page: 1, ord: 0, text: "old"}},
		[]stagedPage{{page: 1, engine: "vision"}}, nil); err != nil {
		t.Fatal(err)
	}
	// Reingest (atomic swap): different page set → prior provenance is replaced.
	if err := s.commitDoc("file:///d", "D",
		[]stagedFrag{{page: 2, ord: 0, text: "new"}},
		[]stagedPage{{page: 2, engine: "text"}}, nil); err != nil {
		t.Fatal(err)
	}
	_, pages, _ := s.DocReview("file:///d")
	if len(pages) != 1 || pages[0].Page != 2 || pages[0].Engine != "text" {
		t.Fatalf("pages after reingest = %+v, want just page 2 (text)", pages)
	}
}
