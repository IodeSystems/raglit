package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iodesystems/raglit"
)

// The property the whole lane split exists for, through the real dispatcher: a
// heavy job that takes forever must not delay a light one queued behind it.
//
// This is what the daemon actually did before. runIndexWorkers was ONE goroutine
// calling ProcessOne per index in turn, so a twenty-two page scan held the queue
// — every index's queue, since the loop was shared — for as long as its vision
// calls took. A markdown file needing three seconds of embedding waited out the
// whole OCR.
//
// The heavy job here never finishes: it blocks in the fetch until the test
// releases it. If light work can only proceed once heavy work is done, this test
// times out, which is precisely the old behaviour.
func TestRunLane_LightDrainsWhileHeavyIsStuck(t *testing.T) {
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
	// Queued FIRST, so any single-queue scheduler hands it out before the rest.
	if _, err := st.Enqueue("file:///corpus/twenty-two-page-scan.pdf", ""); err != nil {
		t.Fatal(err)
	}
	const lightJobs = 3
	for _, u := range []string{"file:///a.md", "file:///b.md", "file:///c.md"} {
		if _, err := st.Enqueue(u, ""); err != nil {
			t.Fatal(err)
		}
	}

	release := make(chan struct{})
	var heavyStarted, lightDone atomic.Int32
	done := make(chan struct{}, lightJobs)

	newWorker := func(s *raglit.Store) *raglit.Worker {
		return &raglit.Worker{
			Store: s,
			Fetcher: func(ctx context.Context, url string) (raglit.Fetched, error) {
				if raglit.LaneFor(url) == raglit.LaneHeavy {
					heavyStarted.Add(1)
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
	go runLane(ctx, reg, raglit.LaneHeavy, 1, newWorker)
	go func() {
		// Count completions by polling the queue rather than instrumenting the
		// worker: what matters is that the ROWS reach done, which is what a
		// person watching the queue would see.
		for ctx.Err() == nil {
			jobs, _ := st.Jobs("done", 100)
			if int32(len(jobs)) > lightDone.Load() {
				for i := lightDone.Load(); i < int32(len(jobs)); i++ {
					done <- struct{}{}
				}
				lightDone.Store(int32(len(jobs)))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	go runLane(ctx, reg, raglit.LaneLight, 2, newWorker)

	deadline := time.After(20 * time.Second)
	for i := 0; i < lightJobs; i++ {
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("only %d of %d light jobs finished while the heavy job was stuck "+
				"(heavy started: %d) — light work is queued behind heavy work again",
				lightDone.Load(), lightJobs, heavyStarted.Load())
		}
	}

	// And the heavy job really was in flight the whole time, so the light work
	// did not simply win a race to an idle queue.
	if heavyStarted.Load() == 0 {
		t.Fatal("the heavy job never started — the test proved nothing")
	}
	close(release)
	cancel()
}

// A lane must never mark more jobs `running` than it has slots to run.
//
// The claim is what writes `running` to the row, so a claimer that runs ahead of
// its runners reports work that nothing is doing — the same lie owner_pid exists
// to expose, told by the scheduler itself. Observed on the live daemon before the
// slot token: the heavy lane has ONE slot, was running one job, and reported two,
// because the claimer had already taken the next one and was blocked handing it
// over. A person watching the queue cannot tell that from real concurrency.
func TestRunLane_NeverClaimsMoreThanItCanRun(t *testing.T) {
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
	for i := 0; i < 6; i++ {
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
	const slots = 1
	go runLane(ctx, reg, raglit.LaneHeavy, slots, newWorker)

	// Let it settle, then compare what the QUEUE says against what is running.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running, err := st.Jobs("running", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(running) > slots {
			t.Fatalf("queue reports %d jobs running in a %d-slot lane (actually in flight: %d) "+
				"— the claimer is running ahead of its runners",
				len(running), slots, inFlight.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if inFlight.Load() == 0 {
		t.Fatal("nothing ever ran — the test proved nothing")
	}
	close(release)
	cancel()
}
