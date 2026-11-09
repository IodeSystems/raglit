package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/raglit"
)

// TestWatcherReconcile drives scanHome through create → no-op → modify → add →
// delete and checks the (enqueued, removed) reconciliation each step.
func TestWatcherReconcile(t *testing.T) {
	daemonRoot := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(daemonRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	proj := t.TempDir()
	home := filepath.Join(proj, ".raglit")
	docs := filepath.Join(proj, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(docs, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.md", "alpha")

	cfg := raglit.Config{
		Project: "proj", Watch: true,
		Indexes: map[string]raglit.IndexConfig{
			"docs": {Roots: []raglit.Root{{Path: "./docs", Include: []string{"*.md"}}}},
		},
	}
	if err := raglit.SaveConfig(raglit.Home(home), cfg); err != nil {
		t.Fatal(err)
	}

	w := newWatcher(reg, daemonRoot, time.Second)

	if enq, rm := w.scanHome(home); enq != 1 || rm != 0 {
		t.Fatalf("initial scan = (%d,%d), want (1,0)", enq, rm)
	}
	if enq, rm := w.scanHome(home); enq != 0 || rm != 0 {
		t.Fatalf("unchanged rescan = (%d,%d), want (0,0)", enq, rm)
	}

	// modify a.md — force a distinct mtime so the change is unambiguous.
	write("a.md", "alpha modified")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(a, future, future); err != nil {
		t.Fatal(err)
	}
	if enq, rm := w.scanHome(home); enq != 1 || rm != 0 {
		t.Fatalf("after modify = (%d,%d), want (1,0)", enq, rm)
	}

	// add b.md
	write("b.md", "beta")
	if enq, rm := w.scanHome(home); enq != 1 || rm != 0 {
		t.Fatalf("after add = (%d,%d), want (1,0)", enq, rm)
	}

	// delete a.md
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if enq, rm := w.scanHome(home); enq != 0 || rm != 1 {
		t.Fatalf("after delete = (%d,%d), want (0,1)", enq, rm)
	}
}

// TestWatcherSkipsWhenDisabled: a registered home with watch:false is a no-op.
func TestWatcherSkipsWhenDisabled(t *testing.T) {
	daemonRoot := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(daemonRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	proj := t.TempDir()
	home := filepath.Join(proj, ".raglit")
	docs := filepath.Join(proj, "docs")
	os.MkdirAll(docs, 0o755)
	os.WriteFile(filepath.Join(docs, "a.md"), []byte("x"), 0o644)
	cfg := raglit.Config{
		Project: "proj", Watch: false,
		Indexes: map[string]raglit.IndexConfig{
			"docs": {Roots: []raglit.Root{{Path: "./docs", Include: []string{"*.md"}}}},
		},
	}
	if err := raglit.SaveConfig(raglit.Home(home), cfg); err != nil {
		t.Fatal(err)
	}
	w := newWatcher(reg, daemonRoot, time.Second)
	if enq, rm := w.scanHome(home); enq != 0 || rm != 0 {
		t.Fatalf("watch:false should be a no-op, got (%d,%d)", enq, rm)
	}
}

// projectFixture is a daemon registry and a project with one watched docs
// root.
func projectFixture(t *testing.T) (reg *raglit.Registry, daemonRoot, home, docs string) {
	t.Helper()
	daemonRoot = t.TempDir()
	reg, err := raglit.OpenScopedRegistry(daemonRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	proj := t.TempDir()
	home = filepath.Join(proj, ".raglit")
	docs = filepath.Join(proj, "docs")
	must(t, os.MkdirAll(docs, 0o755))
	cfg := raglit.Config{
		Project: "proj", Watch: true,
		Indexes: map[string]raglit.IndexConfig{
			"docs": {Roots: []raglit.Root{{Path: "./docs", Include: []string{"*.md"}}}},
		},
	}
	must(t, raglit.SaveConfig(raglit.Home(home), cfg))
	return reg, daemonRoot, home, docs
}

func pendingURLs(t *testing.T, st *raglit.Store) map[string]bool {
	t.Helper()
	jobs, err := st.Jobs("pending", 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, j := range jobs {
		out[j.URL] = true
	}
	return out
}

// With run going, a file written under a watched root is queued from the
// fsnotify burst, not from the poll.
func TestWatcherEventsQueueChanges(t *testing.T) {
	reg, daemonRoot, home, docs := projectFixture(t)
	w := newWatcher(reg, daemonRoot, time.Hour) // the poll never fires here
	must(t, w.Add(home))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	st, err := reg.Get("proj__docs")
	if err != nil {
		t.Fatal(err)
	}
	// run subscribes asynchronously; give it the watch before writing.
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		n := len(w.subs)
		w.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no subscription")
		}
		time.Sleep(20 * time.Millisecond)
	}
	p := filepath.Join(docs, "new.md")
	must(t, os.WriteFile(p, []byte("fresh"), 0o644))
	for !pendingURLs(t, st)[p] {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatalf("%s not queued from the watch", p)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// configTargets resolves include and ignore the way PlanSources does.
func TestConfigTargets(t *testing.T) {
	off := false
	cfg := raglit.Config{
		Ignore: []string{"tmp/**"}, Gitignore: &off,
		Indexes: map[string]raglit.IndexConfig{
			"docs": {Include: []string{"*.md"}, Ignore: []string{"old/**"}, Roots: []raglit.Root{
				{Path: "./a"}, {Path: "/abs/b", Include: []string{"*.txt"}, Ignore: []string{"x"}},
			}},
			"code": {Roots: []raglit.Root{{Path: "src"}}},
		},
	}
	ts := configTargets(cfg, "/proj", "ns", "docs")
	if len(ts) != 2 {
		t.Fatalf("targets %+v", ts)
	}
	byDir := map[string]treeTarget{}
	for _, tt := range ts {
		byDir[tt.dir] = tt
	}
	a, b := byDir["/proj/a"], byDir["/abs/b"]
	if a.index != "ns__docs" || len(a.rules.Include) != 1 || a.rules.Include[0] != "*.md" || len(a.rules.Ignore) != 2 || a.rules.Gitignore == nil || *a.rules.Gitignore {
		t.Errorf("a: %+v", a)
	}
	if len(b.rules.Include) != 1 || b.rules.Include[0] != "*.txt" || len(b.rules.Ignore) != 3 {
		t.Errorf("b: %+v", b)
	}
	// The daemon indexes documents of any size (no 2 MiB code-tree default).
	if a.rules.MaxBytes >= 0 || b.rules.MaxBytes >= 0 {
		t.Errorf("MaxBytes a=%d b=%d, want no limit", a.rules.MaxBytes, b.rules.MaxBytes)
	}
}

// Refreshes that race an unwatch leave no subscription behind: after the
// last Remove, a write under the root is not queued.
func TestWatcherConcurrentRefreshUnwatches(t *testing.T) {
	reg, daemonRoot, home, docs := projectFixture(t)
	w := newWatcher(reg, daemonRoot, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for {
		w.mu.Lock()
		up := w.hub != nil
		w.mu.Unlock()
		if up {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				must(t, w.Add(home))
			case 1:
				must(t, w.Remove(home))
			default:
				w.refresh()
			}
		}()
	}
	wg.Wait()
	must(t, w.Remove(home))
	w.refresh()
	w.mu.Lock()
	n := len(w.subs)
	w.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d subscriptions left after Remove", n)
	}
	st, err := reg.Get("proj__docs")
	if err != nil {
		t.Fatal(err)
	}
	// A pass that started before the Remove may still be walking; one that
	// starts after it does nothing.
	time.Sleep(200 * time.Millisecond)
	p := filepath.Join(docs, "late.md")
	must(t, os.WriteFile(p, []byte("late"), 0o644))
	time.Sleep(1500 * time.Millisecond) // past the default debounce
	if pendingURLs(t, st)[p] {
		t.Fatalf("%s queued after unwatch: a subscription leaked", p)
	}
}

// Add with run going indexes the new home in one full pass, done when Add
// returns: not a pass of its own and a second from the new subscription.
func TestWatcherAddOneFullPass(t *testing.T) {
	reg, daemonRoot, home, docs := projectFixture(t)
	must(t, os.WriteFile(filepath.Join(docs, "a.md"), []byte("alpha"), 0o644))
	w := newWatcher(reg, daemonRoot, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for {
		w.mu.Lock()
		up := w.hub != nil
		w.mu.Unlock()
		if up {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	must(t, w.Add(home))
	st, err := reg.Get("proj__docs")
	if err != nil {
		t.Fatal(err)
	}
	if !pendingURLs(t, st)[filepath.Join(docs, "a.md")] {
		t.Fatal("a.md not queued when Add returned")
	}
	time.Sleep(300 * time.Millisecond)
	if n := w.fullPasses.Load(); n != 1 {
		t.Fatalf("%d full passes for one Add, want 1", n)
	}
}

// A file deleted under a watched directory leaves the index from the burst:
// the directory's no-link-out Skip does not drop a path that is gone.
func TestWatcherDirDeleteRemovesDocument(t *testing.T) {
	daemonRoot := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(daemonRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("notes")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	must(t, os.WriteFile(p, []byte("alpha"), 0o644))
	must(t, st.Ingest(context.Background(), raglit.Document{Path: p, Title: "a.md",
		Fragments: []raglit.Fragment{{Text: "alpha"}}}))

	w := newWatcher(reg, daemonRoot, time.Hour) // the poll never fires here
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for {
		w.mu.Lock()
		up := w.hub != nil
		w.mu.Unlock()
		if up {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	must(t, w.AddDir("notes", dir))
	must(t, os.Remove(p))
	deadline := time.Now().Add(10 * time.Second)
	for {
		paths, err := st.DocumentPaths()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(paths, p) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still indexed after its file was deleted: %v", p, paths)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// noLinkOut admits a gone path whose parent is inside the root, and drops
// one whose parent links out.
func TestNoLinkOutGonePath(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	must(t, os.Symlink(out, filepath.Join(root, "link")))
	skip := noLinkOut(real)
	if skip(filepath.Join(root, "gone.md")) || skip(filepath.Join(root, "gone", "deeper.md")) {
		t.Error("a gone path inside the root was dropped")
	}
	if !skip(filepath.Join(root, "link", "gone.md")) {
		t.Error("a gone path under a link out was admitted")
	}
	must(t, os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "dangling")))
	if !skip(filepath.Join(root, "dangling")) {
		t.Error("a dangling link was admitted")
	}
}

// A directory watch whose index was deleted does not make it again: neither
// a pass of its target still in flight nor the degraded-mode scan.
func TestWatcherDirDoesNotRecreateDeletedIndex(t *testing.T) {
	daemonRoot := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(daemonRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	if _, err := reg.Get("notes"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("alpha"), 0o644))
	d := dirWatch{Index: "notes", Dir: dir}
	tt, err := dirTarget(d)
	if err != nil {
		t.Fatal(err)
	}
	must(t, reg.DeleteIndex("notes"))
	w := newWatcher(reg, daemonRoot, time.Hour)
	if _, err := w.indexTarget(context.Background(), d.key(), tt, nil); err == nil {
		t.Error("indexTarget of a deleted index: no error")
	}
	if enq, _ := w.scanDir(d); enq != 0 {
		t.Errorf("scanDir of a deleted index queued %d", enq)
	}
	for _, n := range reg.Names() {
		if n == "notes" {
			t.Fatal("the deleted index was made again")
		}
	}
}
