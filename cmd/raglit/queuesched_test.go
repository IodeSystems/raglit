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
	go runQueue(ctx, reg, 4, newWorker)

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
	go runQueue(ctx, reg, runners, newWorker)

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
