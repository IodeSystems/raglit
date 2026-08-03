package raglit

import (
	"bytes"
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

// A minified bundle is TEXT, so IsOpaque passes it and the reader indexes it
// happily — 289 KB of xterm.js fragmenting into hundreds of chunks of
// unreadable JavaScript, each embedded, answering no question anyone will ask.
// It is git-tracked too, so a caller filtering by .gitignore cannot help.
func TestIsMinified_CatchesMachineOutputWithoutCatchingProse(t *testing.T) {
	long := strings.Repeat("var a=1,b=2,c=3;function f(){return a+b+c}", 400) // ~16k, one line
	if !IsMinified([]byte(long)) {
		t.Error("a 16k single line is machine output")
	}
	// Measured on the file that prompted this: no newline at all in the head.
	if !IsMinified(bytes.Repeat([]byte("x"), 8000)) {
		t.Error("a head with no line breaks at all is machine output")
	}

	// The false positive that mean-line-length would have caused: markdown
	// written without hard wrapping is ONE LINE PER PARAGRAPH, which lands on
	// the same scale as a minified stylesheet.
	para := strings.Repeat("This is an ordinary sentence of unwrapped prose. ", 20) // ~960 chars
	unwrapped := para + "\n\n" + para + "\n\n" + para + "\n"
	if IsMinified([]byte(unwrapped)) {
		t.Error("unwrapped prose is not minified — max line, not mean, is the signal")
	}
	// Ordinary source and short text are nowhere near.
	for _, ok := range []string{
		"package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
		"# Title\n\nA paragraph.\n\n- a list item\n",
		"a,b,c\n1,2,3\n",
		"",
	} {
		if IsMinified([]byte(ok)) {
			t.Errorf("false positive on %q", ok[:min(len(ok), 20)])
		}
	}
	// A long line that is still plausibly authored stays in.
	if IsMinified([]byte(strings.Repeat("word ", 400))) { // 2000 chars
		t.Error("2k of prose on one line must not be refused; the threshold is 5k")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
