package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Prune removes a local-disk document whose file is gone, with its page images,
// and the page images no document names. An upload is never prunable, even
// with its file gone. Dangling originals/ copies are reported always and
// removed only on request, and an upload's copy stays.
func TestPruneFilesAndUploads(t *testing.T) {
	dir := t.TempDir()
	home := Home(filepath.Join(dir, "home"))
	s, err := OpenHome(home)
	must(t, err)
	defer s.Close()
	ctx := context.Background()

	local := filepath.Join(dir, "corpus", "gone.md")
	must(t, os.MkdirAll(filepath.Dir(local), 0o755))
	must(t, os.WriteFile(local, []byte("x"), 0o644))
	up, err := s.SaveUpload("lost.md", []byte("x"))
	must(t, err)
	upDoc := UploadURL(up)
	for _, p := range []string{local, upDoc} {
		must(t, s.Ingest(ctx, Document{Path: p, Fragments: []Fragment{{Text: "orphan canary"}}}))
		must(t, s.SetDocumentHash(p, "h"))
	}
	img, err := s.savePageImage(local, 1, "image/png", []byte("png"))
	must(t, err)
	var id int64
	must(t, s.db.QueryRow(`SELECT id FROM documents WHERE path = ?`, local).Scan(&id))
	must(t, s.recordPage(id, 1, "tesseract", img))
	stray := filepath.Join(home.PageDir("/no/longer/indexed.pdf"), "p0001.png")
	must(t, os.MkdirAll(filepath.Dir(stray), 0o755))
	must(t, os.WriteFile(stray, []byte("png"), 0o644))
	must(t, os.MkdirAll(home.OriginalsDir(), 0o755))
	origLocal, origUp := home.OriginalPath(local), home.OriginalPath(upDoc)
	for _, p := range []string{origLocal, origUp} {
		must(t, os.WriteFile(p, []byte("copy"), 0o644))
	}

	must(t, os.Remove(local))
	must(t, os.Remove(up))

	if n := s.MissingCount(); n != 1 {
		t.Fatalf("MissingCount = %d, want 1 (the upload is not prunable)", n)
	}
	h, err := s.SearchPath("canary", "", 10)
	must(t, err)
	if len(h) != 1 || h[0].Path != upDoc {
		t.Fatalf("search = %+v, want only the upload", h)
	}

	dry, err := s.Prune(true, false)
	must(t, err)
	if dry.Documents != 1 || dry.OrphanPages != 1 || dry.DanglingOriginals != 1 || dry.Removed || dry.OriginalsRemoved {
		t.Fatalf("dry run = %+v", dry)
	}
	for _, p := range []string{img, stray, origLocal, origUp} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("dry run removed %s", p)
		}
	}

	res, err := s.Prune(false, false)
	must(t, err)
	if res.Documents != 1 || !res.Removed || res.OriginalsRemoved {
		t.Fatalf("prune = %+v", res)
	}
	for _, p := range []string{img, stray, home.PageDir(local), filepath.Dir(stray)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("prune left %s", p)
		}
	}
	if _, err := os.Stat(origLocal); err != nil {
		t.Fatalf("prune without --delete-orphan-originals removed a copy")
	}

	res, err = s.Prune(true, true)
	must(t, err)
	if res.DanglingOriginals != 1 || !res.OriginalsRemoved {
		t.Fatalf("delete originals = %+v", res)
	}
	if _, err := os.Stat(origLocal); !os.IsNotExist(err) {
		t.Fatalf("dangling original kept")
	}
	if _, err := os.Stat(origUp); err != nil {
		t.Fatalf("an upload's copy was removed")
	}
	if st, _ := s.IndexStatus(); st.Documents != 1 {
		t.Fatalf("documents = %d, want the upload", st.Documents)
	}
}

// Forget deletes an upload's file with its document, and never a file on the
// user's disk.
func TestRemoveDocumentStoredFile(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenHome(Home(filepath.Join(dir, "home")))
	must(t, err)
	defer s.Close()
	ctx := context.Background()

	local := filepath.Join(dir, "mine.md")
	must(t, os.WriteFile(local, []byte("x"), 0o644))
	up, err := s.SaveUpload("notes/a.md", []byte("x"))
	must(t, err)
	for _, p := range []string{local, UploadURL(up)} {
		must(t, s.Ingest(ctx, Document{Path: p, Fragments: []Fragment{{Text: "forget canary"}}}))
		must(t, s.RemoveDocument(p, true))
	}
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("forget removed a file on the user's disk: %v", err)
	}
	if _, err := os.Stat(up); !os.IsNotExist(err) {
		t.Fatalf("forget kept the upload's file")
	}
	if st, _ := s.IndexStatus(); st.Documents != 0 {
		t.Fatalf("documents = %d, want 0", st.Documents)
	}
}
