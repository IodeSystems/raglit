package raglit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// KindUnknown meant two things — "text I do not recognise" and "a compiled
// binary" — and the reader treated them the same, because reading bytes as text
// never fails. A 27 MB ELF indexed cleanly and reported done.
func TestIsOpaque_TellsBinaryFromTextWithNoExtensionToGoOn(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// An ELF header: no extension, no magic raglit knows, and the exact shape
	// that got indexed as text.
	elf := append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, make([]byte, 200)...)
	if !IsOpaque(read(write("dun", elf))) {
		t.Error("an executable must be refused, not read as text")
	}
	// Source and prose are not opaque, whatever their extension.
	for _, body := range []string{
		"package main\n\nfunc main() {}\n",
		"# Title\n\nSome prose about the thing.\n",
		"a,b,c\n1,2,3\n",
	} {
		if IsOpaque([]byte(body)) {
			t.Errorf("text was called opaque: %q", body[:12])
		}
	}
	// Formats raglit CAN read must never be refused by this check.
	if IsOpaque([]byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")) {
		t.Error("a PDF must not be refused")
	}
	if IsOpaque([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0}) {
		t.Error("a PNG must not be refused")
	}
	// Nothing to judge is not a refusal.
	if IsOpaque(nil) {
		t.Error("empty input must not be refused here")
	}
}

// The guard must only apply where the reader would otherwise guess. Anything the
// extension or the magic bytes already identified keeps its route.
func TestIsOpaque_OnlyGuardsTheUnknownPath(t *testing.T) {
	if k := ClassifyDoc("report.pdf", ""); k != KindPDF {
		t.Fatalf("extension routing changed: %v", k)
	}
	if k := ClassifyDoc("notes.md", ""); k != KindText {
		t.Fatalf("extension routing changed: %v", k)
	}
	if k := ClassifyDoc("dun", ""); k != KindUnknown {
		t.Fatalf("an extensionless file should still be KindUnknown, got %v", k)
	}
	_ = strings.TrimSpace
}
