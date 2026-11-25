package raglit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestReembedQueue_PauseHoldsIndex is the F51 composition test: pausing an
// index that is re-embedding stops its re-embed for the duration of the pause
// and resumes it on unpause.
//
// It composes the parts that are each tested elsewhere — Store.SetPaused/
// Paused (indexcontrol_test.go) and Store.BackfillVectors returning 0 while
// paused (TestPausedIndexClaimsNothing) — into the queue behaviour the spec
// promises: the queue HOLDS the paused index in the front (Progress still names
// it, its remaining count is untouched, Backfill makes no progress) and RESUMES
// it when it is unpaused (Backfill fills it and the queue advances past it).
func TestReembedQueue_PauseHoldsIndex(t *testing.T) {
	ctx := context.Background()
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	emb := NewEmbedder(&fakeVecClient{}, "m")
	reg.SetEmbedder(emb)
	for _, n := range []string{"default", "alpha", "beta", "gamma"} {
		s, err := reg.Get(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Ingest(ctx, Document{Path: n + ".md", Title: n,
			Fragments: []Fragment{{Text: "token refresh"}, {Text: "deploy rollback"}}}); err != nil {
			t.Fatal(err)
		}
		// Every index now has fragments with no vector: a re-embed to do.
		if _, err := s.DropVectors(); err != nil {
			t.Fatal(err)
		}
	}
	q := NewReembedQueue(reg)

	// Progress names the current index (first in the sorted registry order),
	// its remaining count, and the waiting list.
	p := q.Progress()
	if p.Current != "alpha" || p.Remaining != 2 {
		t.Fatalf("progress = %+v, want current=alpha remaining=2", p)
	}
	if len(p.Waiting) != 3 || p.Waiting[0] != "beta" || p.Waiting[1] != "default" || p.Waiting[2] != "gamma" {
		t.Fatalf("waiting = %v, want [beta default gamma]", p.Waiting)
	}

	// Pausing the current index stops its re-embed: Backfill returns without
	// embedding (it holds the paused index) and makes no progress.
	st, _ := reg.Get("alpha")
	if err := st.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := q.Backfill(ctx, 8); err != nil {
		t.Fatalf("backfill while paused: %v", err)
	}

	// And the queue HOLDS it: Progress still names it, its remaining count is
	// untouched, and it is not advanced past.
	if p = q.Progress(); p.Current != "alpha" || p.Remaining != 2 {
		t.Fatalf("progress while paused = %+v, want current=alpha remaining=2 (held)", p)
	}
	if len(p.Waiting) != 3 || p.Waiting[0] != "beta" {
		t.Fatalf("waiting while paused = %v, want the three still waiting", p.Waiting)
	}

	// SetPriority moves an index to the front of the queue, making it current.
	q.SetPriority("gamma")
	if p = q.Progress(); p.Current != "gamma" || len(p.Waiting) != 3 || p.Waiting[0] != "alpha" || p.Waiting[1] != "beta" {
		t.Fatalf("after SetPriority(gamma) = %+v, want current=gamma waiting [alpha beta default]", p)
	}
	// The current index cannot be reordered out from under the in-flight run.
	q.SetPriority("gamma")
	if p = q.Progress(); p.Current != "gamma" {
		t.Fatalf("SetPriority(current) moved it: %+v", p)
	}

	// Unpausing RESUMES it: Backfill fills the held index and the queue
	// advances past it.
	if err := st.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	if err := q.Backfill(ctx, 8); err != nil {
		t.Fatalf("backfill after unpause: %v", err)
	}
	if p = q.Progress(); p.Current != "" || len(p.Waiting) != 0 {
		t.Fatalf("after resume = %+v, want the run finished, nothing waiting", p)
	}
	// The held index is fully re-embedded, not left in partial search.
	if mv, err := st.MissingVectors(); err != nil || mv != 0 {
		t.Fatalf("default missing vectors after resume = %d, %v; want 0", mv, err)
	}
}

// TestReembedProgress_NamesCurrentAndWaiting is the F51 composition test for
// "re-embed progress is visible": given a re-embed running over 3 indexes,
// Progress names the index currently re-embedding, states its remaining
// fragment count, and names the indexes still waiting.
func TestReembedProgress_NamesCurrentAndWaiting(t *testing.T) {
	ctx := context.Background()
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg.SetEmbedder(NewEmbedder(&fakeVecClient{}, "m"))

	// Three indexes with fragments that have no vector. The registry may also
	// hold a default index; the queue walks every index in sorted order.
	for _, n := range []string{"alpha", "beta", "gamma"} {
		st, err := reg.Get(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Ingest(ctx, Document{Path: n + ".md", Title: n,
			Fragments: []Fragment{{Text: "a"}, {Text: "b"}, {Text: "c"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DropVectors(); err != nil {
			t.Fatal(err)
		}
	}

	q := NewReembedQueue(reg)

	// Progress names the current index (first in sorted order), its remaining
	// fragment count, and the waiting list.
	p := q.Progress()
	if p.Current != "alpha" {
		t.Fatalf("current = %q, want %q", p.Current, "alpha")
	}
	if p.Remaining != 3 {
		t.Fatalf("remaining = %d, want 3", p.Remaining)
	}
	// The waiting list names every index after the current one, in order.
	// With alpha current, beta and gamma are the two we created; the registry
	// may also hold a default index, so the waiting list has 2 or 3 entries.
	// What the spec requires: beta and gamma are both named.
	foundBeta, foundGamma := false, false
	for _, w := range p.Waiting {
		if w == "beta" {
			foundBeta = true
		}
		if w == "gamma" {
			foundGamma = true
		}
	}
	if !foundBeta || !foundGamma {
		t.Fatalf("waiting = %v, want both beta and gamma named", p.Waiting)
	}
}

// TestReembedPriority_MovesToFront is the F51 composition test for "an index
// can be given priority in the re-embed queue": given 3 indexes queued in
// order A, B, C, giving B priority makes the order B, A, C.
func TestReembedPriority_MovesToFront(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg.SetEmbedder(NewEmbedder(&fakeVecClient{}, "m"))

	for _, n := range []string{"alpha", "beta", "gamma"} {
		st, err := reg.Get(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Ingest(context.Background(), Document{Path: n + ".md", Title: n,
			Fragments: []Fragment{{Text: "x"}, {Text: "y"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DropVectors(); err != nil {
			t.Fatal(err)
		}
	}

	q := NewReembedQueue(reg)

	// Initial order: alpha first (sorted), beta and gamma after.
	if p := q.Progress(); p.Current != "alpha" {
		t.Fatalf("initial current = %q, want alpha", p.Current)
	}

	// Give beta priority: it moves to the front, becoming current.
	q.SetPriority("beta")

	p := q.Progress()
	if p.Current != "beta" {
		t.Fatalf("after SetPriority(beta): current = %q, want beta", p.Current)
	}
	// The waiting list has alpha and gamma in order (plus any default index).
	foundAlpha, foundGamma := false, false
	for _, w := range p.Waiting {
		if w == "alpha" {
			foundAlpha = true
		}
		if w == "gamma" {
			foundGamma = true
		}
	}
	if !foundAlpha || !foundGamma {
		t.Fatalf("after SetPriority(beta): waiting = %v, want both alpha and gamma", p.Waiting)
	}
	// Alpha comes before gamma in the waiting list.
	ai, gi := -1, -1
	for i, w := range p.Waiting {
		if w == "alpha" {
			ai = i
		}
		if w == "gamma" {
			gi = i
		}
	}
	if ai > gi {
		t.Fatalf("after SetPriority(beta): alpha at %d is after gamma at %d; want alpha before gamma", ai, gi)
	}
}

// blockingVecClient blocks Embed calls once armed, so a test can park the
// backfill loop inside its slow step — the exact window the backfill race lives
// in — and run SetPriority deterministically in that window. `blocked` is
// closed once the client has entered its first armed block. Every armed call
// blocks on `release`, so the in-flight index cannot finish and advance the
// queue while SetPriority is in flight. When not armed, calls pass through to
// fakeVecClient (for the Ingest setup phase).
type blockingVecClient struct {
	fakeVecClient
	armed       atomic.Int32 // 0 = pass through, 1 = block
	blockedOnce sync.Once
	blocked     chan struct{} // closed when the client enters its first armed block
	release     chan struct{} // closed to let every blocked call go
}

func (c *blockingVecClient) arm() {
	c.armed.Store(1)
}

func (c *blockingVecClient) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	if c.armed.Load() == 0 {
		return c.fakeVecClient.Embed(ctx, model, input)
	}
	c.blockedOnce.Do(func() { close(c.blocked) })
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.fakeVecClient.Embed(ctx, model, input)
}

// TestReembedBackfill_SetPriorityInWindow is the regression test for the
// backfill race: the lock is released between reading q.order[0] and the
// advance q.order = q.order[1:]. A SetPriority that lands in that window must
// not be silently lost.
//
// Without the fix: after SetPriority("beta") the queue is [beta, alpha,
// gamma]. Backfill finishes alpha and its unconditional advance (order[1:])
// removes beta, leaving [alpha, gamma]: beta is dropped from the run.
// With the fix: the advance re-checks that q.order[0] is still the index it
// read; beta is now the front, so the advance is skipped and the next
// iteration picks beta up. The run finishes with every index re-embedded.
//
// This discriminates: on the unfixed code beta is missing from the final
// queue and the test fails; on the fixed code the queue is empty and the test
// passes.
func TestReembedBackfill_SetPriorityInWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bv := &blockingVecClient{
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
	reg.SetEmbedder(NewEmbedder(bv, "m"))
	for _, n := range []string{"alpha", "beta", "gamma"} {
		st, err := reg.Get(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Ingest(ctx, Document{Path: n + ".md", Title: n,
			Fragments: []Fragment{{Text: "token refresh"}, {Text: "deploy rollback"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DropVectors(); err != nil {
			t.Fatal(err)
		}
	}
	q := NewReembedQueue(reg)

	bv.arm()
	done := make(chan error, 1)
	go func() {
		done <- q.Backfill(ctx, 8)
	}()

	// The first embed call is now blocked inside BackfillVectors for alpha.
	<-bv.blocked

	// SetPriority lands in the race window: alpha is current, so the queue
	// becomes [beta, alpha, gamma].
	q.SetPriority("beta")

	// Release the embedder so BackfillVectors completes, the inner loop
	// exhausts alpha, and the advance executes.
	close(bv.release)
	if err := <-done; err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// The run must be finished: every index re-embedded, queue empty.
	p := q.Progress()
	if p.Current != "" || len(p.Waiting) != 0 {
		t.Fatalf("after backfill: current=%q waiting=%v; want empty (run finished)", p.Current, p.Waiting)
	}
	// Every index must be fully re-embedded (no missing vectors).
	for _, n := range []string{"alpha", "beta", "gamma"} {
		st, err := reg.Get(n)
		if err != nil {
			t.Fatal(err)
		}
		if mv, err := st.MissingVectors(); err != nil || mv != 0 {
			t.Fatalf("%s missing vectors = %d, %v; want 0", n, mv, err)
		}
	}
}
