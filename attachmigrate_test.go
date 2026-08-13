package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The migration must move the DOCUMENT, not just the file.
//
// An extracted attachment is indexed: it has fragments, a caption, a history and
// possibly notes. Its path is its identity, and originals/ and pages/ are both
// keyed by that path — so moving the bytes and leaving the row behind turns it
// into `missing-file`, and re-ingesting at the new path abandons everything
// anybody recorded about it.
func TestMigrateExtractedAttachments_MovesTheDocumentNotJustTheFile(t *testing.T) {
	home := Home(t.TempDir())
	s, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// An archive with an attachment extracted the OLD way, beside it.
	corpus := t.TempDir()
	archive := filepath.Join(corpus, "thread.eml")
	if err := os.WriteFile(archive, []byte("From: a@b\r\n\r\nbody\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldDir := LegacyAttachmentDir(archive)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldDir, "p01-01-survey.pdf")
	if err := os.WriteFile(oldPath, []byte("%PDF-1.7 survey"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, manifestName), []byte("# manifest"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Indexed at the old path, with something a person would hate to lose.
	if err := s.Ingest(ctx, Document{Path: oldPath, Title: "Survey",
		Fragments: []Fragment{{Text: "the survey of lot 14"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(oldPath, Note{Body: "signed copy", Author: "carl"}); err != nil {
		t.Fatal(err)
	}
	// And the derived files raglit keys by that path.
	if err := os.MkdirAll(s.home.PageDir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.home.PageDir(oldPath), "p1.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A dry run touches nothing.
	plan, err := s.MigrateExtractedAttachments(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 {
		t.Fatalf("dry run planned %d move(s), want 1", len(plan))
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("a dry run moved a file")
	}

	moves, err := s.MigrateExtractedAttachments(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 || moves[0].Err != "" {
		t.Fatalf("migration: %+v", moves)
	}
	newPath := moves[0].NewPath

	// The bytes moved, into raglit's storage and out of the corpus.
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("the attachment is not at its new path: %v", err)
	}
	if _, err := os.Stat(oldPath); err == nil {
		t.Fatal("the attachment is still in the corpus")
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Fatal("the corpus sidecar directory was left behind")
	}

	// The DOCUMENT moved: same row, same note, searchable at the new path.
	docs, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("want one document after the migration, got %d — it was duplicated", len(docs))
	}
	if docs[0].Path != newPath {
		t.Fatalf("document path is %q, want %q", docs[0].Path, newPath)
	}
	notes, err := s.Notes(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Body != "signed copy" {
		t.Fatalf("the note did not follow the document: %+v", notes)
	}
	hits, _ := s.Search("survey", 5)
	if len(hits) == 0 || hits[0].Path != newPath {
		t.Fatalf("not searchable at the new path: %+v", hits)
	}

	// And the derived files followed, or the document silently loses its pages.
	if _, err := os.Stat(filepath.Join(s.home.PageDir(newPath), "p1.png")); err != nil {
		t.Fatalf("page images did not follow the document: %v", err)
	}

	// Re-running is safe: nothing left to do, nothing broken.
	again, err := s.MigrateExtractedAttachments(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a second run found %d move(s), want none", len(again))
	}
}
