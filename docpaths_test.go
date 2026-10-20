package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageRelOwnHomeCopiedHomeAndCorpus(t *testing.T) {
	home := Home(filepath.Join(t.TempDir(), "indexes", "case"))
	st, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	abs, _ := filepath.Abs(string(home))
	cases := []struct {
		path, want string
		ok         bool
	}{
		{filepath.Join(abs, "attachments", "x.eml", "a.pdf"), "attachments/x.eml/a.pdf", true},
		// A home copied from elsewhere: the rows still name the original home.
		{"/home/u/.raglit/indexes/case/attachments/x.eml/a.pdf", "attachments/x.eml/a.pdf", true},
		{"/home/u/.raglit/indexes/other/attachments/a.pdf", "", false},
		{"/home/u/projects/case/records/a.pdf", "", false},
		{"relative/a.pdf", "", false},
	}
	for _, c := range cases {
		got, ok := st.StorageRel(c.path)
		if got != c.want || ok != c.ok {
			t.Errorf("StorageRel(%q) = %q,%v; want %q,%v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestDocumentPaths(t *testing.T) {
	st, err := OpenHome(Home(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []string{"/c/a.md", "/c/b.md"} {
		if err := st.Ingest(context.Background(), Document{Path: p, Fragments: []Fragment{{Text: "words " + p}}}); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := st.DocumentPaths()
	if err != nil || len(paths) != 2 {
		t.Fatalf("DocumentPaths = %v, %v", paths, err)
	}
}

// A page image recorded under a home that has since moved is served from this
// home's pages/ when the same file is there, and left alone when it is not.
func TestPageImagePathReroots(t *testing.T) {
	home := Home(t.TempDir())
	st, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	doc := "/corpus/scan.pdf"
	if err := st.Ingest(context.Background(), Document{Path: doc, Fragments: []Fragment{{Text: "scanned words", Page: 1}}}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.db.QueryRow(`SELECT id FROM documents WHERE path = ?`, doc).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tagDir := filepath.Base(home.PageDir(doc))
	here := filepath.Join(st.PagesRoot(), tagDir, "p0001.png")
	if err := os.MkdirAll(filepath.Dir(here), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(here, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join("/old/home/indexes/case/pages", tagDir, "p0001.png")
	if err := st.recordPage(id, 1, "vlm", old); err != nil {
		t.Fatal(err)
	}
	if got, err := st.PageImagePath(doc, 1); err != nil || got != here {
		t.Fatalf("PageImagePath = %q, %v; want %q", got, err, here)
	}
	// No such file under this home: the stored path stands.
	missing := "/old/home/pages/nope/p0002.png"
	if err := st.recordPage(id, 2, "vlm", missing); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PageImagePath(doc, 2); got != missing {
		t.Fatalf("PageImagePath(missing) = %q; want the stored path", got)
	}
}
