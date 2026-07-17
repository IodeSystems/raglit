package raglit

import (
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

	if err := s.Ingest(Document{Path: src, Title: "notes", Fragments: []Fragment{
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
	if err := s.Ingest(Document{Path: "virtual://x", Fragments: []Fragment{{Text: "echo"}}}); err != nil {
		t.Fatalf("synthetic ingest should not error: %v", err)
	}
	if _, err := os.Stat(home.OriginalPath("virtual://x")); !os.IsNotExist(err) {
		t.Fatalf("synthetic doc should store no original")
	}
}
