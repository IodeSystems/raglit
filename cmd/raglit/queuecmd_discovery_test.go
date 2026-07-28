package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Walking a directory must discover every format the extractor can read, not
// just text and PDF. The old walk tested isText||isPDF, so a folder of .docx or
// scanned .tif enqueued nothing while reporting success — the silent-skip that
// makes a corpus look indexed when it is empty.
func TestExpandIngestTargetsDiscoversEveryReadableFormat(t *testing.T) {
	dir := t.TempDir()
	readable := []string{"a.docx", "b.doc", "c.odt", "d.rtf", "e.pptx", "f.html", "g.png", "h.tif", "i.pdf", "j.txt", "k.md"}
	for _, n := range append(readable, "skip.bin", "skip.zip") {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := expandIngestTargets([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, p := range got {
		found[filepath.Base(p)] = true
	}
	for _, n := range readable {
		if !found[n] {
			t.Errorf("%s was not discovered — the extractor reads it but the walk skipped it", n)
		}
	}
	for _, n := range []string{"skip.bin", "skip.zip"} {
		if found[n] {
			t.Errorf("%s was discovered but raglit cannot read it", n)
		}
	}
	// A file named explicitly is queued regardless — the walk filter must not
	// leak into the explicit-path branch.
	one, err := expandIngestTargets([]string{filepath.Join(dir, "skip.bin")})
	if err != nil || len(one) != 1 || !strings.HasSuffix(one[0], "skip.bin") {
		t.Errorf("explicit path should pass through, got %v (err %v)", one, err)
	}
}
