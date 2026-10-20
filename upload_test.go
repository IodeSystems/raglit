package raglit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveUpload_StaysInsideUploads(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	st, _ := reg.Get("u")
	p, err := st.SaveUpload("notes/a.md", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(st.home.UploadsDir())
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		t.Fatalf("upload at %s, outside %s", p, root)
	}
	if b, _ := os.ReadFile(p); string(b) != "hello" {
		t.Fatalf("content %q", b)
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "a//b", "./a", `a\b`, "a/.", "x.upload-tmp"} {
		if _, err := st.SaveUpload(bad, []byte("x")); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

// An upload is ingested like any file the daemon found.
func TestSaveUpload_IngestsAndSearches(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	st, _ := reg.Get("u")
	p, err := st.SaveUpload("mem/narwhal.md", []byte("the narwhal tusk is a tooth"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue(UploadURL(p), ""); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: st}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, _ := st.Search("narwhal", 5)
	if len(h) != 1 || h[0].Path != UploadURL(p) {
		t.Fatalf("hits %+v, want the upload", h)
	}
}
