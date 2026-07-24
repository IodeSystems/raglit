package main

import (
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
