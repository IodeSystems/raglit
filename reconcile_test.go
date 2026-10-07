package raglit

// F12: reconcile a job whose stored source path no longer resolves.
//
// The gate sits in Worker.ingest BEFORE the fetch. A retry of a job whose
// source was deleted must not re-open that path: no fetch, no model call, no
// extraction/OCR error, no queue slot burned re-failing identically. The gate
// answers what the file became — MOVED (the same content is indexed under
// another path: re-point the job and go on) or GONE (complete the job in a
// terminal state, not an error) — and fires ONLY on the missing-path case, so
// a live source path is fetched exactly as before.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// reconcileIngest enqueues url and processes it, returning the finished job.
// It looks the job up BY ID — the id Enqueue returned — because reconciliation
// can re-point a job to a different path, after which its URL is no longer the
// one it was enqueued with.
func reconcileIngest(t *testing.T, s *Store, w *Worker, url, title string) JobInfo {
	t.Helper()
	id, err := s.Enqueue(url, title)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, err := s.Job(id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// Spec 1: a job whose stored source path no longer resolves does not re-open
// that path. The retry consumes no fetch/model work on the stale path and does
// not surface as an extraction/OCR error — it completes in the terminal
// source-deleted state.
func TestReconcile_SourceDeletedCompletesWithoutFetching(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "gone.md")
	if err := os.WriteFile(src, []byte("alpha bravo\n\ncharlie delta"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "file://" + src
	s := testStore(t)
	gone := false
	// Serves the file while it exists; once gone is set, any fetch is a
	// violation — the gate must have stopped it before the fetch.
	w := &Worker{Store: s, Fetcher: func(ctx context.Context, u string) (Fetched, error) {
		if gone {
			t.Fatal("fetch was attempted on a path that no longer resolves")
		}
		return Fetch(ctx, u)
	}}

	j1 := reconcileIngest(t, s, w, url, "")
	if j1.State != "done" || j1.Fragments == 0 {
		t.Fatalf("initial ingest = %+v, want done with fragments", j1)
	}

	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	gone = true
	j2 := reconcileIngest(t, s, w, url, "")

	if j2.State != "done" {
		t.Fatalf("retried job state = %q, want done (terminal, not failed): %+v", j2.State, j2)
	}
	if j2.Mode != "source-deleted" {
		t.Fatalf("retried job mode = %q, want source-deleted", j2.Mode)
	}
	if j2.Fragments != 0 {
		t.Fatalf("source-deleted job indexed %d fragments, want 0", j2.Fragments)
	}
	if j2.Error != "" {
		t.Fatalf("source-deleted job carries an error: %q", j2.Error)
	}
	bad, err := s.Jobs("error", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("a missing source surfaced as a failed import: %q", bad[0].Error)
	}
}

// Spec 2: MOVED. When a file with the same DocumentHash exists at another
// path, reconciliation re-points the job to the new path and a subsequent
// retry ingests it successfully — the content lands in the index under the
// new path, under the job's existing identity (the job's id does not change).
func TestReconcile_MovedFileRepointsAndIngests(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("alpha bravo\n\ncharlie delta"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "file://" + old
	s := testStore(t)
	w := &Worker{Store: s}

	j1 := reconcileIngest(t, s, w, url, "")
	if j1.State != "done" || j1.Fragments == 0 {
		t.Fatalf("initial ingest = %+v, want done with fragments", j1)
	}

	// The file MOVED: same bytes, new path.
	if err := os.Rename(old, filepath.Join(dir, "new.md")); err != nil {
		t.Fatal(err)
	}
	// Index the file at its new path (this is what the watcher does on the
	// move), so the DocumentHash of the old path has a home to be found by.
	newURL := "file://" + filepath.Join(dir, "new.md")
	jNew := reconcileIngest(t, s, w, newURL, "")
	if jNew.State != "done" || jNew.Fragments == 0 {
		t.Fatalf("ingest at new path = %+v, want done with fragments", jNew)
	}

	// Now the retry of the STALE job: its path is gone, but the same content
	// is indexed elsewhere. The re-pointed run lands on bytes the index
	// already holds at the new path, so it completes through the
	// same-content short-circuit — done, no error, no re-fragmenting.
	j2 := reconcileIngest(t, s, w, url, "")
	if j2.State != "done" {
		t.Fatalf("retried stale job state = %q, want done: %+v", j2.State, j2)
	}
	if j2.Mode != "unchanged" {
		t.Fatalf("re-pointed retry mode = %q, want unchanged (short-circuit at the new path)", j2.Mode)
	}
	// The re-pointed run minted no new document identity: the fragments are
	// under the path it was re-pointed to, which jNew already indexed.
	st, _ := s.IndexStatus()
	if st.Documents != 2 {
		t.Fatalf("re-pointed run created a new document: %d docs, want 2 (old + new path)", st.Documents)
	}
	hits, err := s.Search("charlie delta", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("re-pointed content not searchable: hits=%d err=%v", len(hits), err)
	}
	if hits[0].Path != newURL {
		t.Fatalf("fragments landed under %q, want the new path %q", hits[0].Path, newURL)
	}
}

// Spec 5: reconciliation uses DocumentHash, which already short-circuits
// re-ingest of identical content. A re-pointed run of unchanged content is a
// fresh ingest at the new path — it costs exactly what any same-content
// re-ingest at that path already costs: the second run is skipped as
// unchanged, and the fragment count does not move.
func TestReconcile_RepointedRunIsNoWorseThanSameContentReingest(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("alpha bravo\n\ncharlie delta"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "file://" + old
	s := testStore(t)
	w := &Worker{Store: s}

	j1 := reconcileIngest(t, s, w, url, "")
	if j1.State != "done" || j1.Fragments == 0 {
		t.Fatalf("initial ingest = %+v, want done with fragments", j1)
	}
	frags := func() int { st, _ := s.IndexStatus(); return st.Fragments }

	// The file moved, and the new path is indexed (same as the MOVED case).
	newPath := filepath.Join(dir, "new.md")
	if err := os.Rename(old, newPath); err != nil {
		t.Fatal(err)
	}
	newURL := "file://" + newPath
	jNew := reconcileIngest(t, s, w, newURL, "")
	if jNew.State != "done" || jNew.Fragments == 0 {
		t.Fatalf("ingest at new path = %+v, want done with fragments", jNew)
	}
	before := frags()

	// The re-pointed retry of the stale job: unchanged bytes at the new path.
	// It must cost no more than the short-circuit any same-content re-ingest
	// gets — i.e. it completes without re-fragmenting from scratch.
	j2 := reconcileIngest(t, s, w, url, "")
	if j2.State != "done" {
		t.Fatalf("re-pointed retry state = %q, want done: %+v", j2.State, j2)
	}
	if j2.Mode != "unchanged" {
		t.Fatalf("re-pointed retry mode = %q, want unchanged (the same-content short-circuit)", j2.Mode)
	}
	if frags() != before {
		t.Fatalf("re-pointed run re-fragmented unchanged content: %d → %d fragments", before, frags())
	}
}

// Spec 6: jobs whose stored paths still resolve are unaffected. Reconciliation
// only fires on the missing-path case; the happy path is fetched exactly as
// before, and a failure on a LIVE path stays an ordinary failure (not
// reconciled, not source-deleted).
func TestReconcile_HappyPathUntouched(t *testing.T) {
	t.Run("live_path_ingests_normally", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "live.md")
		if err := os.WriteFile(src, []byte("alpha bravo\n\ncharlie delta"), 0o644); err != nil {
			t.Fatal(err)
		}
		url := "file://" + src
		s := testStore(t)
		w := &Worker{Store: s}
		j := reconcileIngest(t, s, w, url, "")
		if j.State != "done" || j.Fragments == 0 {
			t.Fatalf("live-path ingest = %+v, want done with fragments", j)
		}
		if j.Mode == "source-deleted" {
			t.Fatal("a live path was treated as a deleted source")
		}
		hits, _ := s.Search("charlie delta", 5)
		if len(hits) == 0 || hits[0].Path != url {
			t.Fatalf("live-path content not searchable under its URL: %+v", hits)
		}
	})

	t.Run("live_path_fetch_failure_stays_an_ordinary_failure", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "live.md")
		if err := os.WriteFile(src, []byte("alpha bravo"), 0o644); err != nil {
			t.Fatal(err)
		}
		url := "file://" + src
		s := testStore(t)
		w := &Worker{Store: s, Fetcher: func(context.Context, string) (Fetched, error) {
			return Fetched{}, os.ErrPermission
		}}
		j := reconcileIngest(t, s, w, url, "")
		if j.State != "error" {
			t.Fatalf("fetch failure on a live path state = %q, want error: %+v", j.State, j)
		}
		if j.Mode == "source-deleted" {
			t.Fatal("a live-path fetch failure was reconciled as a deleted source")
		}
		if j.Error == "" {
			t.Fatal("fetch failure on a live path carried no error message")
		}
	})

	t.Run("never_ingested_missing_path_is_ordinary_fetch_failure", func(t *testing.T) {
		s := testStore(t)
		w := &Worker{Store: s}
		j := reconcileIngest(t, s, w, "file:///does/not/exist.md", "")
		if j.State != "error" {
			t.Fatalf("missing path never ingested: state = %q, want error (no hash to reconcile): %+v", j.State, j)
		}
		if j.Mode == "source-deleted" {
			t.Fatal("a job with no stored content hash was reconciled as source-deleted")
		}
	})
}
