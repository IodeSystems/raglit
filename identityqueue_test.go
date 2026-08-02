package raglit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// countingChatter answers every call with a valid identity and records the
// HIGH-WATER MARK of concurrent calls — the number the slot budget exists to
// hold down.
type countingChatter struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	calls    int
	delay    time.Duration
}

func (c *countingChatter) ChatStream(_ context.Context, _ []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.mu.Lock()
	c.inFlight++
	c.calls++
	n := c.calls
	if c.inFlight > c.maxSeen {
		c.maxSeen = c.inFlight
	}
	c.mu.Unlock()
	time.Sleep(c.delay) // hold the "slot" long enough for overlap to be observable
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return streamReply(fmt.Sprintf(
		`{"name":"Document number %d, captioned","summary":"A document held in this corpus, summarised for the purpose of this test at sufficient length.","kind":"analysis"}`, n)), nil
}

// storeWithDocs is an in-memory index holding n captionable documents.
func storeWithDocs(t *testing.T, n int) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i := 0; i < n; i++ {
		if err := s.Ingest(context.Background(), Document{
			Path:      fmt.Sprintf("/corpus/scan_%03d.pdf", i),
			Title:     fmt.Sprintf("scan_%03d.pdf", i),
			Fragments: []Fragment{{Page: 1, Ord: 0, Text: psaText}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestIdentityQueue_EnqueueIsIdempotentPerDocument(t *testing.T) {
	s := storeWithDocs(t, 1)
	p := "/corpus/scan_000.pdf"
	first, err := s.EnqueueIdentity(p, false)
	if err != nil || !first {
		t.Fatalf("first enqueue = %v, %v", first, err)
	}
	// A second ask while the first is still pending is not more work — it is the
	// same document, and a duplicate row is a duplicate model call.
	again, err := s.EnqueueIdentity(p, false)
	if err != nil || again {
		t.Fatalf("second enqueue = %v, %v — want it skipped", again, err)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Pending != 1 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	// Once it is finished, asking again is a fresh job.
	job, err := s.claimNextIdentityJob()
	if err != nil || job == nil {
		t.Fatalf("claim = %v, %v", job, err)
	}
	if err := s.finishIdentityJob(job.ID, nil); err != nil {
		t.Fatal(err)
	}
	revived, err := s.EnqueueIdentity(p, true)
	if err != nil || !revived {
		t.Fatalf("re-enqueue after done = %v, %v", revived, err)
	}
}

func TestIdentityWorker_DrainsAtTheSlotBudget(t *testing.T) {
	s := storeWithDocs(t, 8)
	c := &countingChatter{delay: 40 * time.Millisecond}
	s.SetIdentifier(NewIdentifier(c, "m"))
	n, err := s.EnqueueMissingIdentities(false)
	if err != nil || n != 8 {
		t.Fatalf("queued %d, %v", n, err)
	}

	w := &IdentityWorker{Store: s, Slots: 2}
	done, err := w.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if done != 8 {
		t.Fatalf("drained %d, want 8", done)
	}
	// The whole point of the queue: never more requests in the model than the
	// endpoint actually serves.
	c.mu.Lock()
	maxSeen := c.maxSeen
	c.mu.Unlock()
	if maxSeen > 2 {
		t.Errorf("%d concurrent model calls — the slot budget is 2", maxSeen)
	}
	if maxSeen < 2 {
		t.Errorf("never reached 2 concurrent calls (max %d) — the slots are not being used", maxSeen)
	}

	q, err := s.IdentityQueue()
	if err != nil {
		t.Fatal(err)
	}
	if q.Pending != 0 || q.Running != 0 || q.Done != 8 || q.Failed != 0 {
		t.Fatalf("queue after drain = %+v", q)
	}
	missing, err := s.DocumentsMissingIdentity()
	if err != nil || len(missing) != 0 {
		t.Fatalf("%d document(s) still uncaptioned: %v", len(missing), err)
	}
}

// An interrupted sweep leaves the rest of the work queued, not lost. This is the
// property the whole table exists for.
func TestIdentityWorker_StoppingLeavesTheRestQueued(t *testing.T) {
	s := storeWithDocs(t, 6)
	c := &countingChatter{delay: 30 * time.Millisecond}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.EnqueueMissingIdentities(false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &IdentityWorker{Store: s, Slots: 2}
	w.OnDone = func(IdentityJob, DocIdentity, error) { cancel() } // stop after the first
	if _, err := w.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	q, err := s.IdentityQueue()
	if err != nil {
		t.Fatal(err)
	}
	if q.Done+q.Failed == 6 {
		t.Fatal("the whole queue drained; this test cannot say anything about resuming")
	}
	if q.Pending+q.Running == 0 {
		t.Fatalf("nothing left queued after an interrupted sweep: %+v", q)
	}
	// Resuming finishes the rest, and does not redo what was done.
	before := q.Done
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	q2, err := s.IdentityQueue()
	if err != nil {
		t.Fatal(err)
	}
	if q2.Pending != 0 || q2.Done < before {
		t.Fatalf("resume left %+v (was %+v)", q2, q)
	}
}

// A document somebody already answered for costs no model call, and its row
// still closes — otherwise every sweep re-asks the same questions forever.
func TestIdentityWorker_SkipsWhatIsAlreadyAnswered(t *testing.T) {
	s := storeWithDocs(t, 2)
	ctx := context.Background()
	if _, err := s.RecordIdentity(ctx, "/corpus/scan_000.pdf",
		DocIdentity{Name: "Mine", Summary: "A caption a person wrote, long enough to be real.", Kind: "deed"}, "carl"); err != nil {
		t.Fatal(err)
	}
	c := &countingChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))
	// Queue everything, including the one a person named.
	if _, err := s.EnqueueIdentityFor([]string{"/corpus/scan_000.pdf", "/corpus/scan_001.pdf"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	calls := c.calls
	c.mu.Unlock()
	if calls != 1 {
		t.Errorf("model calls = %d, want 1 — a person's caption must not be re-asked", calls)
	}
	id, err := s.DocumentIdentity("/corpus/scan_000.pdf")
	if err != nil || !id.ByPerson() || id.Name != "Mine" {
		t.Fatalf("person's caption = %+v, %v", id, err)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Pending != 0 || q.Failed != 0 {
		t.Fatalf("queue = %+v, %v — a skip is not a failure and not a retry", q, err)
	}
}

// A row left 'running' by a process that is gone is work nobody is doing.
func TestReclaimIdentityJobs_RequeuesOrphans(t *testing.T) {
	s := storeWithDocs(t, 1)
	if _, err := s.EnqueueIdentity("/corpus/scan_000.pdf", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimNextIdentityJob(); err != nil {
		t.Fatal(err)
	}
	// Re-stamp the claim as a pid that cannot be alive.
	if _, err := s.db.Exec(`UPDATE identity_jobs SET owner_pid = 2147483647 WHERE state='running'`); err != nil {
		t.Fatal(err)
	}
	n, err := s.ReclaimIdentityJobs()
	if err != nil || n != 1 {
		t.Fatalf("reclaimed %d, %v", n, err)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Pending != 1 || q.Running != 0 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	// Our OWN running rows are not reclaimed out from under us.
	if _, err := s.claimNextIdentityJob(); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReclaimIdentityJobs(); err != nil || n != 0 {
		t.Fatalf("reclaimed a live worker's own row: %d, %v", n, err)
	}
}

// A failure is recorded on the row with its reason, and does not stop the sweep.
func TestIdentityWorker_RecordsAFailureAndCarriesOn(t *testing.T) {
	s := storeWithDocs(t, 2)
	// A model that never returns valid JSON: every document fails identically.
	s.SetIdentifier(NewIdentifier(&identityChatter{replies: []string{"no."}}, "m"))
	if _, err := s.EnqueueMissingIdentities(false); err != nil {
		t.Fatal(err)
	}
	n, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("worked %d jobs, want 2 — a failure must not stop the queue", n)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Failed != 2 || q.Pending != 0 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	jobs, err := s.IdentityJobs("error", 10)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("error jobs = %d, %v", len(jobs), err)
	}
	if !strings.Contains(jobs[0].Error, "identity") {
		t.Errorf("the row does not say why it failed: %q", jobs[0].Error)
	}
}

// A document with nothing to read is SKIPPED, not failed — and a later sweep
// does not queue it again. Counting it as a failure puts a permanent red number
// on a corpus that is fine, and re-queueing it makes work that can never
// succeed look outstanding forever.
func TestIdentityWorker_NothingToCaptionIsSkippedNotFailed(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "/corpus/blank.pdf", Title: "blank.pdf",
		Fragments: []Fragment{{Page: 1, Ord: 0, Text: "p. 3"}}}); err != nil {
		t.Fatal(err)
	}
	c := &countingChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if n, err := s.EnqueueMissingIdentities(false); err != nil || n != 1 {
		t.Fatalf("queued %d, %v", n, err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	calls := c.calls
	c.mu.Unlock()
	if calls != 0 {
		t.Errorf("model calls = %d — an empty document should never reach the model", calls)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Skipped != 1 || q.Failed != 0 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	jobs, err := s.IdentityJobs("skipped", 5)
	if err != nil || len(jobs) != 1 || !strings.Contains(jobs[0].Error, "too little to identify") {
		t.Fatalf("skipped rows = %+v, %v", jobs, err)
	}
	// The next sweep leaves it alone.
	if n, err := s.EnqueueMissingIdentities(false); err != nil || n != 0 {
		t.Fatalf("re-queued %d, %v — a skip must not come back", n, err)
	}
	// --force still reaches it: "nothing to read" stops being true after a re-OCR.
	if n, err := s.EnqueueMissingIdentities(true); err != nil || n != 1 {
		t.Fatalf("--force queued %d, %v", n, err)
	}
}
