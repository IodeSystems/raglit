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

func TestWorker_DedupSkipsUnchangedContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "n.md")
	os.WriteFile(src, []byte("alpha bravo\n\ncharlie delta"), 0o644)
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	url := "file://" + src
	w := &Worker{Store: s} // offline (no segmenter)

	drain := func() JobInfo {
		if _, err := s.Enqueue(url, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		jobs, _ := s.Jobs("all", 10) // newest first
		return jobs[0]
	}

	// 1) first ingest: real work (offline split), fragments indexed.
	j1 := drain()
	if j1.Mode != "offline" || j1.Fragments == 0 {
		t.Fatalf("first ingest = %+v, want offline with fragments", j1)
	}
	frags := func() int { st, _ := s.IndexStatus(); return st.Fragments }
	before := frags()

	// 2) re-ingest identical bytes: skipped, no new work.
	j2 := drain()
	if j2.Mode != "unchanged" {
		t.Fatalf("unchanged re-ingest mode = %q, want unchanged", j2.Mode)
	}
	if frags() != before {
		t.Fatalf("skip should not change the index: %d → %d fragments", before, frags())
	}

	// 3) change the file: re-ingested (hash differs).
	os.WriteFile(src, []byte("echo foxtrot golf"), 0o644)
	j3 := drain()
	if j3.Mode == "unchanged" {
		t.Fatal("changed content must re-ingest, not skip")
	}
	if h, _ := s.Search("foxtrot", 5); len(h) != 1 {
		t.Fatalf("changed content should be searchable: %d hits", len(h))
	}
}
