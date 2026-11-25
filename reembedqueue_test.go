package raglit

import (
	"context"
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
	q := newReembedQueue(reg)

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

	// SetPriority moves a waiting index to the front; the current index is
	// unchanged.
	q.SetPriority("gamma")
	if p = q.Progress(); p.Current != "alpha" || len(p.Waiting) != 3 || p.Waiting[0] != "gamma" || p.Waiting[1] != "beta" {
		t.Fatalf("after SetPriority(gamma) = %+v, want waiting [gamma beta default]", p)
	}
	// The current index cannot be reordered out from under the in-flight run.
	q.SetPriority("alpha")
	if p = q.Progress(); p.Current != "alpha" {
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
