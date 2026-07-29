package raglit

import (
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Without this the queue inflates every time anything requeues — a watch fires,
// a batch is re-run, a sweep is retried. Measured on the corpus that motivated
// it: 162 queued rows for 115 distinct documents, one court memorandum queued
// FIVE times, ~29% of a nine-hour backlog that was pure repetition.
func TestEnqueueDoesNotQueueTheSameUrlTwice(t *testing.T) {
	s := testStore(t)
	first, err := s.Enqueue("file:///a.pdf", "A")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Enqueue("file:///a.pdf", "A")
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("second Enqueue made a new job %d; want the existing %d", again, first)
	}
	other, err := s.Enqueue("file:///b.pdf", "B")
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Error("a different url collapsed onto an existing job")
	}
}

// Re-indexing a document that has CHANGED is legitimate and must stay possible.
// Deduping against done would make a corpus permanently un-refreshable.
func TestAFinishedUrlCanBeQueuedAgain(t *testing.T) {
	s := testStore(t)
	first, err := s.Enqueue("file:///a.pdf", "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE ingest_jobs SET state='done' WHERE id=?`, first); err != nil {
		t.Fatal(err)
	}
	again, err := s.Enqueue("file:///a.pdf", "A")
	if err != nil {
		t.Fatal(err)
	}
	if again == first {
		t.Error("a completed url could not be queued again — re-indexing a changed file must stay possible")
	}
}

func TestDedupeQueueCollapsesAnAlreadyInflatedQueue(t *testing.T) {
	s := testStore(t)
	// Simulate a queue that grew before Enqueue deduped.
	for range 4 {
		if _, err := s.db.Exec(
			`INSERT INTO ingest_jobs (url,title,state,enqueued_at) VALUES (?,?,'pending',0)`,
			"file:///dup.pdf", "dup"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(
		`INSERT INTO ingest_jobs (url,title,state,enqueued_at) VALUES (?,?,'pending',0)`,
		"file:///solo.pdf", "solo"); err != nil {
		t.Fatal(err)
	}
	n, err := s.DedupeQueue()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("removed %d rows, want 3", n)
	}
	var pending int
	if err := s.db.QueryRow(`SELECT count(*) FROM ingest_jobs WHERE state='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Errorf("%d pending after dedupe, want 2 (one per url)", pending)
	}
}

// A worker owns a running job. Deleting a row out from under one is how a job
// ends up stuck in `running` forever with nothing left to finish it.
func TestDedupeLeavesRunningJobsAlone(t *testing.T) {
	s := testStore(t)
	if _, err := s.db.Exec(
		`INSERT INTO ingest_jobs (url,title,state,enqueued_at) VALUES (?,?,'running',0)`,
		"file:///live.pdf", "live"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DedupeQueue(); err != nil {
		t.Fatal(err)
	}
	var running int
	if err := s.db.QueryRow(`SELECT count(*) FROM ingest_jobs WHERE state='running'`).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 1 {
		t.Errorf("dedupe removed a running job")
	}
}
