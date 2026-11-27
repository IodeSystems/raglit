package raglit

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A file under uploads/ that no document row names is named in the report as
// an orphan upload, on both a dry run and a real one. A file a document names
// is not an orphan.
func TestPruneReportsOrphanUploads(t *testing.T) {
	home := Home(filepath.Join(t.TempDir(), "home"))
	s, err := OpenHome(home)
	must(t, err)
	defer s.Close()

	orphan := filepath.Join(home.UploadsDir(), "stray.md")
	must(t, os.MkdirAll(filepath.Dir(orphan), 0o755))
	must(t, os.WriteFile(orphan, []byte("x"), 0o644))
	kept, err := s.SaveUpload("notes/kept.md", []byte("x"))
	must(t, err)
	must(t, s.Ingest(context.Background(), Document{
		Path: UploadURL(kept), Fragments: []Fragment{{Text: "kept upload"}},
	}))

	res, err := s.Prune(true, true)
	must(t, err)
	if res.OrphanUploads != 1 || res.OrphanUploadBytes == 0 ||
		len(res.OrphanUploadFiles) != 1 || res.OrphanUploadFiles[0] != orphan {
		t.Fatalf("dry run = %+v, want orphan upload %s named", res, orphan)
	}

	res, err = s.Prune(false, true)
	must(t, err)
	if res.OrphanUploads != 1 || len(res.OrphanUploadFiles) != 1 || res.OrphanUploadFiles[0] != orphan {
		t.Fatalf("prune = %+v, want orphan upload %s named", res, orphan)
	}
	if st, _ := s.IndexStatus(); st.Documents != 1 {
		t.Fatalf("documents = %d, want the kept upload", st.Documents)
	}
}

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

// A document in raglit's own storage whose source bytes live under uploads/:
// when prune removes dangling originals/ copies, the upload's source bytes are
// left on disk. prune has no delete path for an upload's bytes.
func TestPruneNeverRemovesUploadBytes(t *testing.T) {
	home := Home(filepath.Join(t.TempDir(), "home"))
	s, err := OpenHome(home)
	must(t, err)
	defer s.Close()

	up, err := s.SaveUpload("notes/doc.md", []byte("source bytes"))
	must(t, err)
	doc := UploadURL(up)
	must(t, s.Ingest(context.Background(), Document{
		Path: doc, Fragments: []Fragment{{Text: "upload doc"}},
	}))
	// A dangling copy under originals/ that no document names — the kind of file
	// --delete-orphan-originals removes.
	stray := filepath.Join(home.OriginalsDir(), "dangling-original.bin")
	must(t, os.MkdirAll(filepath.Dir(stray), 0o755))
	must(t, os.WriteFile(stray, []byte("dangling"), 0o644))

	res, err := s.Prune(false, true)
	must(t, err)
	if !res.OriginalsRemoved {
		t.Fatalf("prune = %+v, want originals removed", res)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("dangling original %s survived prune", stray)
	}
	if b, err := os.ReadFile(up); err != nil || string(b) != "source bytes" {
		t.Fatalf("upload's source bytes removed by prune: %v %q", err, b)
	}
	if res.OrphanUploads != 0 {
		t.Fatalf("prune = %+v, want the named upload not counted orphan", res)
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
