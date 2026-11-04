package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iodesystems/raglit"
)

// The property the scheduler exists for, through the real runner pool: a
// document stuck on ONE model must not hold up documents that need a different
// one.
//
// The daemon has had three answers to this. First a single serial loop, where a
// forty-minute scan held every index's queue. Then two lanes with slot counts,
// which fixed the symptom by classifying work as heavy or light. Now per-model
// admission, which fixes the cause: what serialised a transcription against an
// embedding was never that they were the same KIND of work, it was one shared
// "the GPU admits one" budget covering models that live on different cards.
//
// The slow document here never finishes — it blocks inside the fetch until the
// test releases it. If the rest can only proceed once it is done, this test
// times out, which is precisely the old behaviour.
func TestRunQueue_OneStuckDocumentDoesNotStallTheRest(t *testing.T) {
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}

	// Queued FIRST, so any scheduler handing work out in order reaches it before
	// the rest.
	if _, err := st.Enqueue("file:///corpus/twenty-two-page-scan.pdf", ""); err != nil {
		t.Fatal(err)
	}
	const others = 3
	for i := 0; i < others; i++ {
		if _, err := st.Enqueue(fmt.Sprintf("file:///corpus/notes-%d.md", i), ""); err != nil {
			t.Fatal(err)
		}
	}

	release := make(chan struct{})
	var stuckStarted atomic.Int32
	newWorker := func(s *raglit.Store) *raglit.Worker {
		return &raglit.Worker{
			Store: s,
			Fetcher: func(ctx context.Context, url string) (raglit.Fetched, error) {
				// Stands in for a long vision call: the scan blocks, everything
				// else returns immediately.
				if raglit.LaneFor(url) == raglit.LaneHeavy {
					stuckStarted.Add(1)
					select {
					case <-release:
					case <-ctx.Done():
					}
					return raglit.Fetched{}, context.Canceled
				}
				return raglit.Fetched{Data: []byte("a light document, indexed in milliseconds")}, nil
			},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runQueue(ctx, ctx, reg, 4, newWorker)

	// Establish the premise before testing the conclusion: the slow document must
	// really be in flight, or "the others finished" says nothing.
	for start := time.Now(); stuckStarted.Load() == 0; {
		if time.Since(start) > 10*time.Second {
			t.Fatal("the slow document never started — the test cannot prove anything")
		}
		time.Sleep(10 * time.Millisecond)
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		done, err := st.Jobs("done", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(done) >= others {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d fast documents finished while one was stuck — "+
				"work is queued behind a document it has nothing to do with", len(done), others)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stuckStarted.Load() != 1 {
		t.Fatalf("the slow document ran %d times", stuckStarted.Load())
	}
	close(release)
	cancel()
}

// The queue must never mark more jobs `running` than it has runners.
//
// The claim is what writes `running` to the row, so a claimer that runs ahead of
// its runners reports work that nothing is doing — the same lie owner_pid exists
// to expose, told by the scheduler itself. Observed on the live daemon: a
// one-slot lane ran one job and reported two.
func TestRunQueue_NeverClaimsMoreThanItCanRun(t *testing.T) {
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := st.Enqueue(fmt.Sprintf("file:///scan-%d.pdf", i), ""); err != nil {
			t.Fatal(err)
		}
	}

	release := make(chan struct{})
	var inFlight atomic.Int32
	newWorker := func(s *raglit.Store) *raglit.Worker {
		return &raglit.Worker{
			Store: s,
			Fetcher: func(ctx context.Context, _ string) (raglit.Fetched, error) {
				inFlight.Add(1)
				defer inFlight.Add(-1)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return raglit.Fetched{}, context.Canceled
			},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const runners = 2
	go runQueue(ctx, ctx, reg, runners, newWorker)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running, err := st.Jobs("running", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(running) > runners {
			t.Fatalf("queue reports %d jobs running with %d runners (actually in flight: %d) "+
				"— the claimer is running ahead of its runners",
				len(running), runners, inFlight.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if inFlight.Load() == 0 {
		t.Fatal("nothing ever ran — the test proved nothing")
	}
	close(release)
	cancel()
}

// drainQueue starts runQueue on one index with the two contexts the daemon
// wires, and returns the jobDrain over it.
func drainQueue(t *testing.T, reg *raglit.Registry, newWorker func(*raglit.Store) *raglit.Worker) *jobDrain {
	t.Helper()
	work, cutOff := context.WithCancel(context.Background())
	claim, stopClaims := context.WithCancel(work)
	t.Cleanup(cutOff)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runQueue(claim, work, reg, 1, newWorker)
	}()
	return &jobDrain{stopClaims: stopClaims, cutOff: cutOff, done: done}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for start := time.Now(); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func jobCount(t *testing.T, st *raglit.Store, state string) int {
	t.Helper()
	js, err := st.Jobs(state, 20)
	if err != nil {
		t.Fatal(err)
	}
	return len(js)
}

// A stop lets the running job finish and claims nothing after it: the job ends
// 'done', and the job behind it stays 'pending' for the next process.
func TestJobDrain_RunningJobFinishesAndNothingNewIsClaimed(t *testing.T) {
	reg, err := raglit.OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := st.Enqueue(fmt.Sprintf("file:///notes-%d.md", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	var started atomic.Int32
	d := drainQueue(t, reg, func(s *raglit.Store) *raglit.Worker {
		return &raglit.Worker{Store: s, Fetcher: func(ctx context.Context, _ string) (raglit.Fetched, error) {
			started.Add(1)
			<-release
			return raglit.Fetched{Data: []byte("a short note")}, nil
		}}
	})
	waitFor(t, "the first job to start", func() bool { return started.Load() == 1 })

	drained := make(chan bool)
	go func() { drained <- d.drain(10 * time.Second) }()
	// Give the claimer a chance to take the second job if it still could.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if !<-drained {
		t.Fatal("drain reported runners still running")
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("%d jobs started; the drain must claim nothing after the stop", n)
	}
	if done, pending := jobCount(t, st, "done"), jobCount(t, st, "pending"); done != 1 || pending != 1 {
		t.Fatalf("done=%d pending=%d, want 1 and 1", done, pending)
	}
}

// A job still running when the grace ends is cancelled and goes back on the
// queue. Before the drain it was cut off by the exit and marked 'error' at the
// next start.
func TestJobDrain_JobCutOffAtGraceIsRequeued(t *testing.T) {
	reg, err := raglit.OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue("file:///scan.pdf", ""); err != nil {
		t.Fatal(err)
	}
	var started atomic.Int32
	d := drainQueue(t, reg, func(s *raglit.Store) *raglit.Worker {
		return &raglit.Worker{Store: s, Fetcher: func(ctx context.Context, _ string) (raglit.Fetched, error) {
			started.Add(1)
			<-ctx.Done()
			return raglit.Fetched{}, ctx.Err()
		}}
	})
	waitFor(t, "the job to start", func() bool { return started.Load() == 1 })

	if !d.drain(50 * time.Millisecond) {
		t.Fatal("drain reported runners still running after the cut-off")
	}
	if pending, failed := jobCount(t, st, "pending"), jobCount(t, st, "error"); pending != 1 || failed != 0 {
		t.Fatalf("pending=%d error=%d, want the cut-off job back as pending", pending, failed)
	}
}
