package raglit

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two subscribers of one root share one watcher and each get the burst; the
// last unsubscribe stops it.
func TestWatchHubSharesOneWatcher(t *testing.T) {
	root := t.TempDir()
	h := NewWatchHub(fastWatch)
	t.Cleanup(h.Stop)
	a, b := newBurstLog(), newBurstLog()
	ua, err := h.Subscribe(root, TreeRules{Include: []string{"*.md"}}, a.fn)
	if err != nil {
		t.Fatal(err)
	}
	w := h.watcher(root)
	ub, err := h.Subscribe(root+"/", TreeRules{}, b.fn)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil || h.watcher(root) != w {
		t.Fatal("second subscriber got its own watcher")
	}
	writeFiles(t, root, map[string]string{"x.md": "x", "y.txt": "y"})
	if c := a.wait(t, 2*time.Second); len(c.Paths) != 1 || c.Paths[0] != filepath.Join(root, "x.md") {
		t.Errorf("a (*.md) got %+v", c)
	}
	if c := b.wait(t, 2*time.Second); len(c.Paths) != 2 {
		t.Errorf("b (all) got %+v", c)
	}
	ua()
	ua()
	select {
	case <-w.done:
		t.Fatal("stopped with a subscriber left")
	default:
	}
	ub()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("last unsubscribe did not stop the watcher")
	}
	if h.watcher(root) != nil {
		t.Error("hub still holds the stopped watcher")
	}
}

// Concurrent Index calls for one key share runs: one run in flight, and every
// call that arrives during it shares exactly one follow-up.
func TestWatchHubIndexSingleFlight(t *testing.T) {
	h := NewWatchHub(WatchOptions{})
	var runs, inFlight, maxInFlight atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 10)
	do := func(context.Context) (TreeResult, error) {
		n := runs.Add(1)
		if f := inFlight.Add(1); f > maxInFlight.Load() {
			maxInFlight.Store(f)
		}
		started <- struct{}{}
		<-release
		inFlight.Add(-1)
		return TreeResult{Queued: int(n)}, nil
	}
	ctx := context.Background()
	results := make(chan TreeResult, 10)
	var wg sync.WaitGroup
	call := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := h.Index(ctx, "root", do)
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	call()
	<-started // run 1 is going
	for range 5 {
		call()
	}
	time.Sleep(100 * time.Millisecond) // let them all join the follow-up
	release <- struct{}{}              // run 1 ends; the follow-up starts
	<-started
	release <- struct{}{}
	wg.Wait()
	close(results)
	if runs.Load() != 2 || maxInFlight.Load() != 1 {
		t.Fatalf("runs=%d maxInFlight=%d, want 2 runs one at a time", runs.Load(), maxInFlight.Load())
	}
	byRun := map[int]int{}
	for r := range results {
		byRun[r.Queued]++
	}
	if byRun[1] != 1 || byRun[2] != 5 {
		t.Errorf("results by run %v, want run 1 to one caller and run 2 to five", byRun)
	}
	// Idle again: the next call runs at once.
	go func() { <-started; release <- struct{}{} }()
	if r, _ := h.Index(ctx, "root", do); r.Queued != 3 {
		t.Errorf("third run result %+v", r)
	}
}

// A waiter that gives up does not cancel the run; Stop does, and waits for
// it. After Stop, Index fails.
func TestWatchHubStopCancelsRuns(t *testing.T) {
	h := NewWatchHub(WatchOptions{})
	started := make(chan struct{})
	var ended atomic.Bool
	do := func(ctx context.Context) (TreeResult, error) {
		close(started)
		<-ctx.Done()
		ended.Store(true)
		return TreeResult{}, ctx.Err()
	}
	ctx, giveUp := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := h.Index(ctx, "root", do); errc <- err }()
	<-started
	giveUp()
	if err := <-errc; err == nil {
		t.Fatal("a waiter whose ctx ended got no error")
	}
	time.Sleep(50 * time.Millisecond)
	if ended.Load() {
		t.Fatal("the waiter's cancel cancelled the run")
	}
	h.Stop()
	if !ended.Load() {
		t.Fatal("Stop returned before the run ended")
	}
	if _, err := h.Index(context.Background(), "root", do); err == nil {
		t.Error("Index after Stop: want an error")
	}
	if _, err := h.Subscribe(t.TempDir(), TreeRules{}, func(Changes) {}); err == nil {
		t.Error("Subscribe after Stop: want an error")
	}
}

// Once unsubscribe returns, the subscriber gets no callback, even with
// another subscriber keeping the watcher going.
func TestWatchHubUnsubscribeWaitsForDelivery(t *testing.T) {
	root := t.TempDir()
	h := NewWatchHub(fastWatch)
	t.Cleanup(h.Stop)
	in, release := make(chan struct{}, 10), make(chan struct{})
	var after atomic.Bool
	var unsubbed atomic.Bool
	ua, err := h.Subscribe(root, TreeRules{}, func(Changes) {
		if unsubbed.Load() {
			after.Store(true)
		}
		in <- struct{}{}
		<-release
	})
	if err != nil {
		t.Fatal(err)
	}
	ub, err := h.Subscribe(root, TreeRules{}, func(Changes) {})
	if err != nil {
		t.Fatal(err)
	}
	defer ub()
	writeFiles(t, root, map[string]string{"a.md": "a"})
	<-in
	done := make(chan struct{})
	go func() { ua(); unsubbed.Store(true); close(done) }()
	select {
	case <-done:
		t.Fatal("unsubscribe returned during a delivery")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	writeFiles(t, root, map[string]string{"b.md": "b"})
	time.Sleep(300 * time.Millisecond)
	if after.Load() {
		t.Fatal("callback after unsubscribe returned")
	}
}
