package raglit

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A document whose file is gone drops out of search at once and is listed,
// grouped by the top-most deleted directory, until Prune removes it. A document
// a library caller ingested under a name, not a file, is never prunable.
func TestPruneMissingFiles(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	dir := t.TempDir()
	keep := filepath.Join(dir, "a", "keep.md")
	deep := filepath.Join(dir, "wt", "agent-1", "src", "deep.md")
	loose := filepath.Join(dir, "loose.md")
	for _, f := range []string{keep, deep, loose} {
		must(t, os.MkdirAll(filepath.Dir(f), 0o755))
		must(t, os.WriteFile(f, []byte("x"), 0o644))
	}
	for _, p := range []string{keep, deep, loose, keep + "#p1-2", "/no/such/library.md"} {
		must(t, s.Ingest(ctx, Document{Path: p, Fragments: []Fragment{{Text: "worktree prune canary"}}}))
	}
	// What the ingest pipeline stamps on a document it read from a file.
	for _, p := range []string{keep, deep, loose, keep + "#p1-2"} {
		must(t, s.SetDocumentHash(p, "h"))
	}

	must(t, os.RemoveAll(filepath.Join(dir, "wt")))
	must(t, os.Remove(loose))

	h, err := s.SearchPath("canary", "", 10)
	must(t, err)
	var got []string
	for _, x := range h {
		got = append(got, x.Path)
	}
	want := []string{keep, keep + "#p1-2", "/no/such/library.md"}
	if !sameSet(got, want) {
		t.Fatalf("search = %q, want %q", got, want)
	}

	if n := s.MissingCount(); n != 2 {
		t.Fatalf("MissingCount = %d, want 2", n)
	}
	dry, err := s.Prune(true, false)
	must(t, err)
	wantDirs := []PruneDir{{Dir: dir, Documents: 1}, {Dir: filepath.Join(dir, "wt"), Documents: 1}}
	if dry.Documents != 2 || dry.Removed || !reflect.DeepEqual(dry.Dirs, wantDirs) {
		t.Fatalf("dry run = %+v, want 2 documents in %+v, nothing removed", dry, wantDirs)
	}
	if st, _ := s.IndexStatus(); st.Documents != 5 {
		t.Fatalf("a dry run removed documents: %d left", st.Documents)
	}
	res, err := s.Prune(false, false)
	must(t, err)
	if res.Documents != 2 || !res.Removed {
		t.Fatalf("prune = %+v", res)
	}
	if st, _ := s.IndexStatus(); st.Documents != 3 {
		t.Fatalf("after prune: %d documents, want 3", st.Documents)
	}
	if n := s.MissingCount(); n != 0 {
		t.Fatalf("MissingCount after prune = %d", n)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
