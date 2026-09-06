package raglit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure this exists for. The page cache is keyed by the image's SHA and
// nothing else, so re-indexing renders the same pixels, computes the same key,
// and gets the same answer. Five documents in a real corpus were re-indexed to
// fix watermark-only reads; four "completed" in twenty seconds and not one byte
// changed. Purging is the only thing that makes the ordinary path do the work
// again.
func TestPurgeRefusesANonRasterisedDocument(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	txt := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Text, office and email never touch the page cache. Reporting a confident
	// zero would read as "nothing to fix" when the truth is "this is not how
	// this format fails".
	if _, err := s.PurgeDocPageCache(t.Context(), txt); err == nil {
		t.Error("purging a text document reported success; it has no cached pages to purge")
	}
}

func TestSplitTranscriptionPagesReadsTheMarkerNumbers(t *testing.T) {
	const sc = "# Transcription\n\n---\n\n## Page 1\n\nfirst\n\n## Page 7\n\nseventh\n"
	got := splitTranscriptionPages(sc)
	if len(got) != 2 {
		t.Fatalf("got %d page(s), want 2", len(got))
	}
	// The marker's own number, not a count — a sidecar may start at a page other
	// than one, and counting would mislabel everything after a gap.
	if got[0].page != 1 || got[1].page != 7 {
		t.Errorf("pages = %d, %d; want 1, 7", got[0].page, got[1].page)
	}
	if !strings.Contains(got[1].text, "seventh") {
		t.Errorf("page 7 body = %q", got[1].text)
	}
}

// The sweep must find a watermark-only page and must NOT flag a survey sheet
// whose content is a described figure.
func TestSuspectDocsFindsBadPagesAndSkipsFigures(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "order.pdf"+transcriptionSuffix)
	if err := os.WriteFile(bad, []byte(
		"# Transcription\n\n---\n\n## Page 1\n\nT\n   EN\n  M\n CU\nDO\n AL\n CI\nFI\nOF\nUN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fig := filepath.Join(root, "survey.pdf"+transcriptionSuffix)
	if err := os.WriteFile(fig, []byte(
		"# Transcription\n\n---\n\n## Page 1\n\nSHEET 1\nOF 2\n\n> **drawing:** A record of survey showing the lots.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SuspectDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if pages, ok := got[filepath.Join(root, "order.pdf")]; !ok || len(pages) != 1 || pages[0] != 1 {
		t.Errorf("watermark page not reported: %v", got)
	}
	if _, ok := got[filepath.Join(root, "survey.pdf")]; ok {
		t.Error("a page whose content is a described figure was reported as suspect")
	}
}
