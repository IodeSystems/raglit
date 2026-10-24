package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultHome_EnvOverride(t *testing.T) {
	t.Setenv("RAGLIT_HOME", "/tmp/custom-raglit")
	if got := DefaultHome(); got != Home("/tmp/custom-raglit") {
		t.Fatalf("RAGLIT_HOME ignored: %s", got)
	}
	t.Setenv("RAGLIT_HOME", "")
	// Falls back to <userhome>/local/raglit.
	if got := DefaultHome(); filepath.Base(string(got)) != "raglit" {
		t.Fatalf("unexpected default home: %s", got)
	}
}

func TestDiscoverHome_WalkUp(t *testing.T) {
	t.Setenv("RAGLIT_HOME", "") // don't let a real env leak in
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	dotraglit := filepath.Join(proj, ProjectHomeName)
	sub := filepath.Join(proj, "a", "b")
	for _, d := range []string{dotraglit, sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// From a nested subdir, DiscoverHome walks up to the project's .raglit/.
	t.Chdir(sub)
	got := DiscoverHome()
	// EvalSymlinks: macOS temp dirs are symlinked (/var → /private/var).
	want, _ := filepath.EvalSymlinks(dotraglit)
	gotEval, _ := filepath.EvalSymlinks(string(got))
	if gotEval != want {
		t.Fatalf("DiscoverHome = %s, want %s", got, dotraglit)
	}
}

func TestDiscoverHome_FallsBackToDefault(t *testing.T) {
	t.Setenv("RAGLIT_HOME", "/tmp/fallback-raglit")
	dir := t.TempDir() // no .raglit anywhere up the tree
	t.Chdir(dir)
	if got := DiscoverHome(); got != Home("/tmp/fallback-raglit") {
		t.Fatalf("DiscoverHome = %s, want the DefaultHome fallback", got)
	}
}

// A local-disk document is read from its file, not copied: nothing writes
// originals/ (USER 2026-10-24), and the home no longer creates it.
func TestOpenHome_CopiesNoOriginals(t *testing.T) {
	dir := t.TempDir()
	home := Home(filepath.Join(dir, "home"))
	src := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(src, []byte("alpha bravo\n\ncharlie delta"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if fi, err := os.Stat(home.PagesDir()); err != nil || !fi.IsDir() {
		t.Fatalf("home layout missing dir %s: %v", home.PagesDir(), err)
	}
	if err := s.Ingest(context.Background(), Document{Path: src, Title: "notes", Fragments: []Fragment{
		{Ord: 0, Text: "alpha bravo"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home.OriginalsDir()); !os.IsNotExist(err) {
		t.Fatalf("originals/ exists after an ingest: %v", err)
	}
}
