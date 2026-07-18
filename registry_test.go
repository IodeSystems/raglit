package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistry_NamedIndexesAreSeparate(t *testing.T) {
	home := Home(filepath.Join(t.TempDir(), "h"))
	reg, err := OpenRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx := context.Background()

	def, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	code, err := reg.Get("code")
	if err != nil {
		t.Fatal(err)
	}
	if def == code {
		t.Fatal("default and code should be distinct stores")
	}

	must(t, def.Ingest(ctx, Document{Path: "note.md", Fragments: []Fragment{{Text: "the alpha document"}}}))
	must(t, code.Ingest(ctx, Document{Path: "main.go", Fragments: []Fragment{{Text: "the beta source file"}}}))

	// Each index only sees its own docs.
	if h, _ := def.Search("beta", 5); len(h) != 0 {
		t.Fatalf("default should not see code's docs: %+v", h)
	}
	if h, _ := code.Search("alpha", 5); len(h) != 0 {
		t.Fatalf("code should not see default's docs: %+v", h)
	}
	if h, _ := code.Search("beta", 5); len(h) != 1 {
		t.Fatalf("code should find its own doc: %+v", h)
	}

	// Names reflects both; a distinct sqlite file exists for the named index.
	names := reg.Names()
	if !contains2(names, "default") || !contains2(names, "code") {
		t.Fatalf("Names missing entries: %v", names)
	}
	if _, err := os.Stat(filepath.Join(string(home), "index-code.sqlite")); err != nil {
		t.Fatalf("named index file not created: %v", err)
	}

	// Get is cached (same handle).
	if again, _ := reg.Get("code"); again != code {
		t.Fatal("Get should cache the opened store")
	}
}

func TestRegistry_NameSanitized(t *testing.T) {
	home := Home(filepath.Join(t.TempDir(), "h"))
	reg, _ := OpenRegistry(home)
	defer reg.Close()
	// A traversal-y name must be sanitized to a safe file, still under the home.
	if _, err := reg.Get("../../etc/passwd"); err != nil {
		t.Fatalf("sanitized name should still open: %v", err)
	}
	entries, _ := os.ReadDir(string(home))
	for _, e := range entries {
		if e.Name() == ".." || filepath.IsAbs(e.Name()) {
			t.Fatalf("unsafe index file created: %s", e.Name())
		}
	}
}

func contains2(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
