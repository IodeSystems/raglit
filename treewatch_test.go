package raglit

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// burstLog collects Changes from a watcher's callback.
type burstLog struct {
	mu  sync.Mutex
	got []Changes
	at  []time.Time
	ch  chan struct{}
}

func newBurstLog() *burstLog { return &burstLog{ch: make(chan struct{}, 100)} }

func (b *burstLog) fn(c Changes) {
	b.mu.Lock()
	b.got = append(b.got, c)
	b.at = append(b.at, time.Now())
	b.mu.Unlock()
	b.ch <- struct{}{}
}

func (b *burstLog) wait(t *testing.T, d time.Duration) Changes {
	t.Helper()
	select {
	case <-b.ch:
	case <-time.After(d):
		t.Fatalf("no burst within %v", d)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.got[len(b.got)-1]
}

func (b *burstLog) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-b.ch:
		b.mu.Lock()
		defer b.mu.Unlock()
		t.Fatalf("unexpected burst %+v", b.got[len(b.got)-1])
	case <-time.After(d):
	}
}

func watchFor(t *testing.T, root string, r TreeRules, o WatchOptions) (*TreeWatcher, *burstLog) {
	t.Helper()
	b := newBurstLog()
	w, err := WatchTree(context.Background(), root, r, o, b.fn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	return w, b
}

var fastWatch = WatchOptions{Quiet: 50 * time.Millisecond, MaxDelay: 2 * time.Second, MinInterval: -1}

// A burst of writes is one Changes, filtered by the rules, with new
// directories watched as they appear.
func TestWatchTreeBurst(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"node_modules/x/a.js": "x"})
	_, b := watchFor(t, root, TreeRules{Include: []string{"*.md"}}, fastWatch)
	writeFiles(t, root, map[string]string{"a.md": "a", "b.md": "b", "c.txt": "c", ".hidden.md": "h", "node_modules/x/b.md": "n"})
	c := b.wait(t, 2*time.Second)
	if c.Root != root || c.Degraded || len(c.Paths) != 2 || c.Paths[0] != filepath.Join(root, "a.md") || c.Paths[1] != filepath.Join(root, "b.md") {
		t.Fatalf("burst %+v", c)
	}
	b.none(t, 200*time.Millisecond)

	must(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	c = b.wait(t, 2*time.Second)
	if len(c.Paths) != 1 || c.Paths[0] != filepath.Join(root, "sub") {
		t.Fatalf("new dir burst %+v", c)
	}
	writeFiles(t, root, map[string]string{"sub/d.md": "d"})
	if c = b.wait(t, 2*time.Second); len(c.Paths) != 1 || c.Paths[0] != filepath.Join(root, "sub/d.md") {
		t.Fatalf("file in new dir %+v", c)
	}
	must(t, os.RemoveAll(filepath.Join(root, "sub")))
	if c = b.wait(t, 2*time.Second); len(c.Paths) == 0 {
		t.Fatalf("remove burst %+v", c)
	}
}

// Events that never go quiet still end a burst at MaxDelay.
func TestWatchTreeMaxDelay(t *testing.T) {
	root := t.TempDir()
	_, b := watchFor(t, root, TreeRules{}, WatchOptions{Quiet: 300 * time.Millisecond, MaxDelay: 400 * time.Millisecond, MinInterval: -1})
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				_ = os.WriteFile(filepath.Join(root, "a.md"), []byte{byte(i)}, 0o644)
			}
		}
	}()
	start := time.Now()
	b.wait(t, 3*time.Second)
	close(stop)
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("first burst after %v; MaxDelay is 400ms", el)
	}
}

// Bursts are at least MinInterval apart.
func TestWatchTreeMinInterval(t *testing.T) {
	root := t.TempDir()
	_, b := watchFor(t, root, TreeRules{}, WatchOptions{Quiet: 20 * time.Millisecond, MaxDelay: time.Second, MinInterval: 500 * time.Millisecond})
	writeFiles(t, root, map[string]string{"a.md": "1"})
	b.wait(t, 2*time.Second)
	writeFiles(t, root, map[string]string{"b.md": "2"})
	b.wait(t, 2*time.Second)
	b.mu.Lock()
	gap := b.at[1].Sub(b.at[0])
	b.mu.Unlock()
	if gap < 450*time.Millisecond {
		t.Errorf("bursts %v apart, want >= MinInterval 500ms", gap)
	}
}

// Rescan reports the whole tree as one burst.
func TestWatchTreeRescan(t *testing.T) {
	root := t.TempDir()
	w, b := watchFor(t, root, TreeRules{}, fastWatch)
	w.Rescan()
	w.Rescan()
	if c := b.wait(t, 2*time.Second); c.Paths != nil || c.Degraded {
		t.Fatalf("rescan burst %+v, want whole tree", c)
	}
	b.none(t, 200*time.Millisecond)
}

// Idle stops the watcher; Touch holds it off.
func TestWatchTreeIdleStop(t *testing.T) {
	root := t.TempDir()
	w, _ := watchFor(t, root, TreeRules{}, WatchOptions{Idle: 300 * time.Millisecond})
	for range 4 {
		time.Sleep(150 * time.Millisecond)
		w.Touch()
	}
	select {
	case <-w.done:
		t.Fatal("stopped while touched")
	default:
	}
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop after Idle")
	}
}

// At the watch limit the watcher reports Degraded once and adds no more.
func TestWatchTreeDegraded(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a/x.md": "x", "b/y.md": "y", "c/z.md": "z"})
	var adds atomic.Int32
	prev := watchAdd
	watchAdd = func(fw *fsnotify.Watcher, dir string) error {
		if adds.Add(1) > 2 {
			return syscall.ENOSPC
		}
		return fw.Add(dir)
	}
	t.Cleanup(func() { watchAdd = prev })
	_, b := watchFor(t, root, TreeRules{}, fastWatch)
	if c := b.wait(t, 2*time.Second); !c.Degraded {
		t.Fatalf("burst %+v, want Degraded", c)
	}
	n := adds.Load()
	must(t, os.MkdirAll(filepath.Join(root, "d"), 0o755))
	time.Sleep(200 * time.Millisecond)
	if adds.Load() != n {
		t.Errorf("added watches after degrading")
	}
}

// Stop from another goroutine during a slow callback waits for it; Stop from
// inside the callback does not deadlock.
func TestWatchTreeStopWaitsForCallback(t *testing.T) {
	root := t.TempDir()
	in, release := make(chan struct{}), make(chan struct{})
	var ended atomic.Bool
	w, err := WatchTree(context.Background(), root, TreeRules{}, fastWatch, func(Changes) {
		close(in)
		<-release
		ended.Store(true)
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{"a.md": "a"})
	<-in
	stopped := make(chan struct{})
	go func() { w.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while the callback was running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-stopped
	if !ended.Load() {
		t.Fatal("callback not finished when Stop returned")
	}

	root2 := t.TempDir()
	var w2 *TreeWatcher
	ready := make(chan struct{})
	w2, err = WatchTree(context.Background(), root2, TreeRules{}, fastWatch, func(Changes) {
		<-ready
		w2.Stop()
	})
	if err != nil {
		t.Fatal(err)
	}
	close(ready)
	writeFiles(t, root2, map[string]string{"b.md": "b"})
	select {
	case <-w2.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop from inside the callback did not end the watcher")
	}
}
