package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFetch_FileURLAndBarePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.md")
	if err := os.WriteFile(p, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"file://" + p, p} {
		f, err := Fetch(context.Background(), u)
		if err != nil {
			t.Fatalf("fetch %q: %v", u, err)
		}
		if string(f.Data) != "hello world" || f.Title != "note.md" || f.IsPDF {
			t.Fatalf("fetch %q → %+v", u, f)
		}
	}
}

func TestFetch_UnsupportedScheme(t *testing.T) {
	if _, err := Fetch(context.Background(), "ftp://x/y"); err == nil {
		t.Fatal("expected unsupported-scheme error")
	}
}

func TestWorker_IngestsQueuedTextURL(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "auth.md")
	if err := os.WriteFile(src, []byte("Access tokens expire.\n\nThe refresh token rotates."), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenHome(Home(filepath.Join(dir, "home")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	url := "file://" + src
	if _, err := s.Enqueue(url, ""); err != nil {
		t.Fatal(err)
	}

	// Before processing: one pending job, nothing indexed.
	st, _ := s.IndexStatus()
	if st.Pending != 1 || st.Documents != 0 {
		t.Fatalf("pre-drain status wrong: %+v", st)
	}

	w := &Worker{Store: s}
	did, err := w.ProcessOne(context.Background())
	if err != nil || !did {
		t.Fatalf("ProcessOne: did=%v err=%v", did, err)
	}

	// After: job done, doc indexed under the URL, searchable.
	st, _ = s.IndexStatus()
	if st.Done != 1 || st.Pending != 0 || st.Documents != 1 {
		t.Fatalf("post-drain status wrong: %+v", st)
	}
	hits, _ := s.Search("refresh token", 5)
	if len(hits) == 0 || hits[0].Path != url {
		t.Fatalf("ingested doc not searchable under its URL: %+v", hits)
	}

	// Queue empty now.
	if did, _ := w.ProcessOne(context.Background()); did {
		t.Fatal("expected empty queue")
	}
}

func TestWorker_PDFWithoutOCRFailsGracefully(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "scan.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4 not really"), 0o644)
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Enqueue("file://"+pdf, "")
	// No OCR configured → job should be marked error, not crash the worker.
	if _, err := (&Worker{Store: s}).ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne returned infra error: %v", err)
	}
	st, _ := s.IndexStatus()
	if st.Failed != 1 {
		t.Fatalf("PDF-without-OCR should fail the job: %+v", st)
	}
}

func TestIndexStatus_ItemsAndCounts(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("content "+name), 0o644)
		s.Enqueue("file://"+p, "")
	}
	st, _ := s.IndexStatus()
	if st.Pending != 3 || len(st.Items) != 3 {
		t.Fatalf("want 3 pending items, got %+v", st)
	}
	// ETA is unknown (0) until a job completes.
	if st.Items[0].ETASeconds != 0 || st.RatePerMin != 0 {
		t.Fatalf("eta/rate should be unknown pre-completion: %+v", st)
	}
	// Drain and re-check.
	n, err := (&Worker{Store: s}).Drain(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("drain: n=%d err=%v", n, err)
	}
	st, _ = s.IndexStatus()
	if st.Done != 3 || st.Pending != 0 || len(st.Items) != 0 {
		t.Fatalf("post-drain: %+v", st)
	}
}
