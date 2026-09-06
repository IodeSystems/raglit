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

func TestOpenHome_StoresOriginals(t *testing.T) {
	dir := t.TempDir()
	home := Home(filepath.Join(dir, "home"))

	// A real source file on disk.
	src := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(src, []byte("alpha bravo\n\ncharlie delta"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Layout was created.
	for _, d := range []string{home.OriginalsDir(), home.PagesDir()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("home layout missing dir %s: %v", d, err)
		}
	}

	if err := s.Ingest(context.Background(), Document{Path: src, Title: "notes", Fragments: []Fragment{
		{Ord: 0, Text: "alpha bravo"},
	}}); err != nil {
		t.Fatal(err)
	}

	// The original was copied into originals/.
	stored := home.OriginalPath(src)
	got, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("original not stored at %s: %v", stored, err)
	}
	if string(got) != "alpha bravo\n\ncharlie delta" {
		t.Fatalf("stored original corrupted: %q", got)
	}

	// A synthetic doc (Path not a real file) ingests fine and stores nothing.
	if err := s.Ingest(context.Background(), Document{Path: "virtual://x", Fragments: []Fragment{{Text: "echo"}}}); err != nil {
		t.Fatalf("synthetic ingest should not error: %v", err)
	}
	if _, err := os.Stat(home.OriginalPath("virtual://x")); !os.IsNotExist(err) {
		t.Fatalf("synthetic doc should store no original")
	}
}
