package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit"
)

// watch DIR --index NAME records the root once, relative when it lies inside
// the project, and turns watching on; unwatch takes it back out.
func TestWatchRootIsRecordedOnce(t *testing.T) {
	proj := t.TempDir()
	home := raglit.Home(filepath.Join(proj, ".raglit"))
	dir := filepath.Join(proj, "docs")
	os.MkdirAll(dir, 0o755)

	added, err := addWatchRoot(home, "notes", dir, "acme")
	if err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	if added, _ := addWatchRoot(home, "notes", dir, "acme"); added {
		t.Error("a second add must change nothing")
	}
	cfg, _, _ := raglit.LoadConfig(home)
	roots := cfg.Indexes["notes"].Roots
	if len(roots) != 1 || roots[0].Path != "docs" || !cfg.Watch || cfg.Project != "acme" {
		t.Fatalf("config = %+v, want one relative root, watch on, project set", cfg)
	}

	if removed, _ := removeWatchRoot(home, "notes", dir); !removed {
		t.Fatal("unwatch must remove the root")
	}
	cfg, _, _ = raglit.LoadConfig(home)
	if len(cfg.Indexes["notes"].Roots) != 0 {
		t.Errorf("roots after unwatch: %v", cfg.Indexes["notes"].Roots)
	}
}

// With no ./.raglit, watch must not write the shared default home: every
// project without one would put its roots in one config. It uses the
// project's own home under the daemon root, and needs a project for that.
func TestWatchHomeNeverTheSharedDefault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RAGLIT_ROOT", root)
	t.Setenv("RAGLIT_HOME", filepath.Join(root, "shared-home"))
	t.Chdir(t.TempDir())

	if _, err := watchHome("", ""); err == nil {
		t.Fatal("no .raglit and no project must be an error")
	}
	h, err := watchHome("", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "projects", "acme"); string(h) != want {
		t.Errorf("home = %s, want %s", h, want)
	}
	if h == raglit.DefaultHome() {
		t.Error("watch chose the shared default home")
	}
}
