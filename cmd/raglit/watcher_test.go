package main

import (
	"context"
	"os"
	"path/filepath"
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
}
