package raglit

import (
	"os"
	"path/filepath"
	"testing"
)

// The failure this exists for: two attachments in ardley-v-brannock had their
// names truncated at 102 characters by the tool that unpacked them, losing the
// ".pdf". Nothing errored — reading bytes as text always "works" — and the index
// filled with %PDF-1.7 and endobj while the order's text stayed unsearchable.
func TestAnExtensionlessPDFIsRoutedByItsBytes(t *testing.T) {
	if got := ClassifyDoc("Fw__Proposed_Order_Granting_Easement_and_Setting_Locati", ""); got != KindUnknown {
		t.Fatalf("precondition: the name alone should say nothing, got %v", got)
	}
	if got := SniffBytes([]byte("%PDF-1.7\n%\xe2\xe3")); got != KindPDF {
		t.Errorf("want KindPDF from the magic bytes, got %v", got)
	}
}

// The extension stays the authority. A .docx and a .odt are both zips and a .md
// and a .csv are both text; the bytes cannot separate those and must not try.
func TestTheExtensionWinsWhenItSaysAnything(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.md")
	// Text content that a naive sniffer could mistake for something else.
	os.WriteFile(p, []byte("# notes\n"), 0o644)
	if got := ClassifyPath(p, ""); got != KindText {
		t.Errorf("an .md must stay text, got %v", got)
	}
}

func TestSniffCoversTheFormatsThatActuallyArriveUnnamed(t *testing.T) {
	for name, c := range map[string]struct {
		head []byte
		want DocKind
	}{
		"pdf":  {[]byte("%PDF-1.4"), KindPDF},
		"png":  {[]byte("\x89PNG\r\n\x1a\n"), KindImage},
		"jpeg": {[]byte{0xFF, 0xD8, 0xFF, 0xE0}, KindImage},
		"ole2": {ole2Magic[:], KindOffice},
		"zip":  {[]byte("PK\x03\x04"), KindUnknown}, // ambiguous on purpose
		"text": {[]byte("Dear Mr."), KindUnknown},
	} {
		if got := SniffBytes(c.head); got != c.want {
			t.Errorf("%s: got %v want %v", name, got, c.want)
		}
	}
}

// A file shorter than a signature must not panic or match by accident.
func TestSniffIsSafeOnTinyFiles(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {'%'}, {'%', 'P'}} {
		if got := SniffBytes(b); got != KindUnknown {
			t.Errorf("%q: got %v, want KindUnknown", b, got)
		}
	}
}
