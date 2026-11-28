package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A Markdown file whose first line is a level-1 heading is titled by it; a
// level-2 heading, prose first, or another format keeps the file name.
func TestFetch_MarkdownHeadingIsTheTitle(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct{ body, want string }{
		"incident.md":    {"\n# Incident report: checkout outage\n\nDuration: 47 minutes.", "Incident report: checkout outage"},
		"sub.md":         {"## Not a title\n\ntext", "sub.md"},
		"prose.md":       {"Some text.\n# Late heading", "prose.md"},
		"notes.txt":      {"# Looks like a heading", "notes.txt"},
		"empty-h1.md":    {"# \n\ntext", "empty-h1.md"},
		"guide.markdown": {"# Guide", "Guide"},
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := Fetch(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if f.Title != c.want {
			t.Errorf("%s: title %q, want %q", name, f.Title, c.want)
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
	// An idle queue must marshal items as [], not null.
	b, err := json.Marshal(st)
	if err != nil || !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("idle status json = %s err=%v", b, err)
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

	// 1) first ingest: real work (deterministic overlap), fragments indexed.
	j1 := drain()
	if j1.Mode != "text-overlap" || j1.Fragments == 0 {
		t.Fatalf("first ingest = %+v, want text-overlap with fragments", j1)
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

// TestWorker_EmptyFileIsFlaggedNotFailed pins the diagnosis AND its shape.
//
// A 0-byte file reaches every extractor and each says something else about it.
// In the fence-dispute corpus a 0-byte .docx failed as `pandoc .docx: exit status 63`,
// which names the tool rather than the problem and reads as a broken install.
// The text path is worse: it does not fail at all, it indexes a document with no
// fragments — the no-fragments health kind, a row that looks like a document
// from every angle except the one that matters.
//
// Having no content is a fact about the DOCUMENT, so the job COMPLETES and the
// emptiness is reported as a problem. Failing it would park the row in the
// failed-jobs list to be retried forever, re-failing identically every time.
func TestWorker_EmptyFileIsFlaggedNotFailed(t *testing.T) {
	for _, name := range []string{"empty.docx", "empty.pdf", "empty.md"} {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			url := "file:///corpus/" + name
			if _, err := s.Enqueue(url, ""); err != nil {
				t.Fatal(err)
			}
			w := &Worker{Store: s, Fetcher: func(context.Context, string) (Fetched, error) {
				return Fetched{Data: []byte{}}, nil
			}}
			if _, err := w.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}

			if bad, err := s.Jobs("error", 10); err != nil {
				t.Fatal(err)
			} else if len(bad) != 0 {
				t.Fatalf("an empty %s was reported as a failed import: %q", name, bad[0].Error)
			}
			done, err := s.Jobs("done", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(done) != 1 || done[0].Mode != "empty" {
				t.Fatalf("want one completed job with mode=empty, got %+v", done)
			}

			// Flagged, not silent.
			probs, err := s.Problems(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var found *Problem
			for i := range probs {
				if probs[i].Kind == ProblemEmptySource {
					found = &probs[i]
				}
			}
			if found == nil {
				t.Fatalf("an empty %s produced no problem row — absent and quiet", name)
			}
			if found.Subject != url {
				t.Fatalf("problem names %q, want %q", found.Subject, url)
			}

			// And it did NOT become a phantom document row, which is the
			// no-fragments trap the text path used to fall into.
			for _, p := range probs {
				if p.Kind == ProblemNoFragments {
					t.Fatalf("an empty file left an indexed-but-unsearchable row: %s", p.Subject)
				}
			}
		})
	}
}

// batchLimitedVecClient counts one token per character — dense text, like a
// CSV — and refuses an input over max with llama.cpp's own wording.
type batchLimitedVecClient struct{ max, refused int }

func (c *batchLimitedVecClient) Embed(_ context.Context, _ string, texts []string) ([][]float32, error) {
	for _, t := range texts {
		if len(t) > c.max {
			c.refused++
			return nil, fmt.Errorf(`llm: input too large for this endpoint (status 500, not retried): {"error":{"code":500,"message":"input (%d tokens) is too large to process. increase the physical batch size (current batch size: %d)","type":"server_error"}}`, len(t), c.max)
		}
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

// An endpoint whose physical batch is smaller than the limit raglit holds
// refuses a dense fragment and names its number. The job learns it, reads the
// document again with fragments cut to fit, and completes with every fragment
// embedded. Measured 2026-10-22: a 75 KB CSV, 13 fragments, one at 5250 tokens
// against a batch of 4096.
func TestWorker_LearnsTheEmbedBatchAndRetries(t *testing.T) {
	s := testStore(t)
	client := &batchLimitedVecClient{max: 4096}
	s.SetEmbedder(NewEmbedder(client, "dense-embed"))
	var b strings.Builder
	for i := 0; b.Len() < 30000; i++ {
		fmt.Fprintf(&b, "%d,%d,%d\n", i, i*7, i*13)
	}
	url := "file:///corpus/survey.csv"
	if _, err := s.Enqueue(url, ""); err != nil {
		t.Fatal(err)
	}
	// A 9000-character window of dense CSV is past 4096 tokens, so the first
	// embed is refused; the default window is too small to reach the limit.
	w := &Worker{Store: s, Frag: FragConfig{Window: 9000, Stride: 6000, Floor: 3000, EmbedLimitTokens: 8192}, Fetcher: func(context.Context, string) (Fetched, error) {
		return Fetched{Data: []byte(b.String())}, nil
	}}
	for range 3 {
		if _, err := w.ProcessOne(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if client.refused == 0 {
		t.Fatal("the endpoint never refused: the test does not exercise the limit")
	}
	if bad, _ := s.Jobs("error", 10); len(bad) != 0 {
		t.Fatalf("job failed: %s", bad[0].Error)
	}
	if w.Frag.EmbedLimitTokens != 4096 || s.LearnedEmbedLimitTokens("dense-embed") != 4096 {
		t.Errorf("limit = %d (worker), %d (stored); want 4096", w.Frag.EmbedLimitTokens, s.LearnedEmbedLimitTokens("dense-embed"))
	}
	if n, _ := s.MissingVectors(); n != 0 {
		t.Errorf("%d fragment(s) without a vector", n)
	}
}

// seedZeroSpanTextDoc writes a text file, ingests it with 0/0 line spans
// (pre-R8), and records the document hash so the worker's fast path would
// normally skip it.
func seedZeroSpanTextDoc(t *testing.T, s *Store, content string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "note.md")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "file://" + src
	hash := sha256hex([]byte(content))
	if err := s.Ingest(context.Background(), Document{
		Path: url, Title: "note.md",
		Fragments: []Fragment{{Page: 0, Ord: 0, Text: content, StartLine: 0, EndLine: 0}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDocumentHash(url, hash); err != nil {
		t.Fatal(err)
	}
	return url
}

func zeroSpanCount(t *testing.T, s *Store, url string) (int, int) {
	t.Helper()
	var zero, total int
	if err := s.db.QueryRow(
		`SELECT SUM(f.start_line = 0 AND f.end_line = 0), COUNT(*)
		   FROM fragments f JOIN documents d ON d.id = f.doc_id
		  WHERE d.path = ? AND f.origin NOT IN ('identity','fields')`, url).
		Scan(&zero, &total); err != nil {
		t.Fatal(err)
	}
	return zero, total
}

// A text document whose fragments all have 0/0 line spans (pre-R8 ingest) is
// re-fragmented on the next unchanged ingest, producing real line spans.
// A document whose fragments already carry real spans is skipped as unchanged.
func TestWorkerBackfillReFragmentsZeroLineSpanTextDocs(t *testing.T) {
	content := "alpha bravo\n\ncharlie delta\necho foxtrot"

	t.Run("zero-span doc is re-fragmented", func(t *testing.T) {
		s := testStore(t)
		url := seedZeroSpanTextDoc(t, s, content)

		zero, total := zeroSpanCount(t, s, url)
		if total == 0 || zero != total {
			t.Fatalf("precondition: want all 0/0, got %d/%d", zero, total)
		}

		if _, err := s.Enqueue(url, ""); err != nil {
			t.Fatal(err)
		}
		w := &Worker{Store: s}
		if _, err := w.ProcessOne(context.Background()); err != nil {
			t.Fatal(err)
		}

		jobs, _ := s.Jobs("all", 10)
		if len(jobs) == 0 {
			t.Fatal("no job recorded")
		}
		if jobs[0].Mode == "unchanged" {
			t.Fatal("a 0/0 text doc was skipped as unchanged; it must be re-fragmented")
		}

		zero, total = zeroSpanCount(t, s, url)
		if total == 0 {
			t.Fatal("no fragments after backfill")
		}
		if zero != 0 {
			t.Fatalf("after backfill: %d of %d fragments still have 0/0 line spans", zero, total)
		}
	})

	t.Run("real-span doc is skipped", func(t *testing.T) {
		s := testStore(t)
		url := seedZeroSpanTextDoc(t, s, content)

		// Simulate a doc that already has real spans: overwrite with a real
		// ingest first, so the fragments carry line numbers.
		if _, err := s.Enqueue(url, ""); err != nil {
			t.Fatal(err)
		}
		w := &Worker{Store: s}
		if _, err := w.ProcessOne(context.Background()); err != nil {
			t.Fatal(err)
		}

		zero, total := zeroSpanCount(t, s, url)
		if total == 0 || zero != 0 {
			t.Fatalf("after first backfill: %d/%d zero-span, want 0/%d", zero, total, total)
		}

		// Second unchanged ingest: must skip.
		if _, err := s.Enqueue(url, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ProcessOne(context.Background()); err != nil {
			t.Fatal(err)
		}
		jobs, _ := s.Jobs("all", 10)
		if len(jobs) == 0 || jobs[0].Mode != "unchanged" {
			t.Fatalf("second ingest mode = %q, want unchanged", jobs[0].Mode)
		}
	})
}

// After the backfill re-fragments a doc to real line spans, a subsequent
// unchanged ingest skips it: the fast path sees real spans and does not
// re-fragment, so the backfill is one-time, not a per-run cost.
func TestWorkerBackfillIsOneTime(t *testing.T) {
	content := "one two three\n\nfour five six"
	s := testStore(t)
	url := seedZeroSpanTextDoc(t, s, content)
	w := &Worker{Store: s}

	// First run: backfill.
	if _, err := s.Enqueue(url, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Jobs("all", 10)
	if len(jobs) == 0 || jobs[0].Mode == "unchanged" {
		t.Fatalf("first run mode = %q, want re-fragmentation", jobs[0].Mode)
	}

	// Second run: skip.
	if _, err := s.Enqueue(url, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs, _ = s.Jobs("all", 10)
	if len(jobs) == 0 {
		t.Fatal("no job on second run")
	}
	if jobs[0].Mode != "unchanged" {
		t.Fatalf("second run mode = %q, want unchanged (backfill is one-time)", jobs[0].Mode)
	}

	// Third run: still skip.
	if _, err := s.Enqueue(url, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs, _ = s.Jobs("all", 10)
	if len(jobs) == 0 || jobs[0].Mode != "unchanged" {
		t.Fatalf("third run mode = %q, want unchanged", jobs[0].Mode)
	}
}
