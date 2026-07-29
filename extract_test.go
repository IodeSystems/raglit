package raglit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyDoc(t *testing.T) {
	cases := []struct {
		name, ct string
		want     DocKind
	}{
		{"report.pdf", "", KindPDF},
		{"x", "application/pdf", KindPDF},
		{"scan.png", "", KindImage},
		{"x", "image/jpeg", KindImage},
		{"paper.docx", "", KindOffice},
		{"book.epub", "", KindOffice},
		{"page.html", "", KindOffice},
		{"x", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", KindOffice},
		{"notes.md", "", KindText},
		{"readme.txt", "", KindText},
		{"x", "text/plain", KindText},
		{"mystery", "", KindUnknown},
	}
	for _, c := range cases {
		if got := ClassifyDoc(c.name, c.ct); got != c.want {
			t.Errorf("ClassifyDoc(%q,%q) = %d, want %d", c.name, c.ct, got, c.want)
		}
	}
}

func TestExtForContentType(t *testing.T) {
	cases := map[string]string{
		"application/pdf": ".pdf",
		"image/png":       ".png",
		"image/jpeg":      ".jpg",
		"text/html":       ".html",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
		"text/plain; charset=utf-8": ".txt",
		"application/x-mystery":     "",
	}
	for mime, want := range cases {
		if got := ExtForContentType(mime); got != want {
			t.Errorf("ExtForContentType(%q) = %q, want %q", mime, got, want)
		}
	}
}

// ExtractPaged routes a text file to the text path (engine "text", no OCR).
func TestExtractPaged_Text(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(fp, []byte("# Title\n\nbody text"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := ExtractPaged(context.Background(), fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Engine != "text" || pages[0].Text != "# Title\n\nbody text" {
		t.Errorf("pages = %+v", pages)
	}
}

// ExtractPaged routes an image to the OCR cascade (here a stub cheap engine),
// tagging the page with the engine that produced it.
func TestExtractPaged_Image(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(fp, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	clean := PageOCR{Text: "invoice total forty two dollars today", MeanConfidence: 0.97, BoxCount: 6}
	ocr := &OCR{Cheap: stubEngine{name: "tesseract", po: clean}}
	pages, err := ExtractPaged(context.Background(), fp, ocr)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Engine != "tesseract" || pages[0].Text != clean.Text {
		t.Errorf("pages = %+v", pages)
	}
}

// A scanned page with no OCR configured is a clear error, not a silent empty.
func TestExtractPaged_ImageNoOCR(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(fp, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractPaged(context.Background(), fp, &OCR{}); err == nil {
		t.Error("want an error: scanned image + no vision/cheap engine")
	}
}

// A legacy binary .doc classifies as KindOffice so it reaches the office path at
// all, but must NOT be handed to pandoc — pandoc cannot read OLE2 and fails with
// an error that reads like file corruption. This pins both halves.
func TestLegacyDocRoutesAwayFromPandoc(t *testing.T) {
	if got := ClassifyDoc("engagement-letter.doc", ""); got != KindOffice {
		t.Errorf("ClassifyDoc(.doc) = %v, want KindOffice", got)
	}
	if got := ClassifyDoc("x", "application/msword"); got != KindOffice {
		t.Errorf("ClassifyDoc(application/msword) = %v, want KindOffice", got)
	}
	if officeExts[".doc"] {
		t.Error(".doc is in officeExts — it would be routed to pandoc, which cannot read it")
	}
	// With no converter installed, the error must name the missing tool rather
	// than surfacing a pandoc parse failure.
	if !HaveLegacyDoc() {
		_, err := OfficeText(context.Background(), "whatever.doc")
		if err == nil {
			t.Fatal("OfficeText(.doc) with no converter: want error, got nil")
		}
		if !strings.Contains(err.Error(), "antiword") {
			t.Errorf("error should name the tool to install, got: %v", err)
		}
	}
}

// The worst corruption found in a live corpus, and it was one wrong measurement.
//
// `pdftotext -layout` pads with spaces to preserve position, so a diagonal
// "UNOFFICIAL DOCUMENT" watermark — eighteen letters spread corner to corner —
// comes back 144 characters long. `len(strings.TrimSpace(t))` counts that padding,
// clears a threshold of 24, and the page is accepted as a real text layer that
// never goes near OCR. Three recorded instruments were "transcribed" to nothing
// but their watermark, with no error, and re-indexing them changed nothing
// because the text layer was being taken every time.
func TestAWatermarkTextLayerDoesNotCountAsContent(t *testing.T) {
	// Verbatim shape of what pdftotext -layout returned for the SJ order.
	watermark := "AL\n  CI\n   FI\n\n     OF\n\n      UN\n\n T\n\n  EN\n\n   M\n    CU\n     DO\n" +
		strings.Repeat(" ", 60)
	if got := textLayerContent(watermark); got >= pdfTextThreshold {
		t.Errorf("content = %d, threshold %d — a watermark passed as a text layer", got, pdfTextThreshold)
	}
	// And the measurement that was being used would have passed it.
	if len(strings.TrimSpace(watermark)) < pdfTextThreshold {
		t.Skip("the old measurement no longer passes this fixture; the regression is gone another way")
	}
}

// A page with even a caption keeps its cheap, exact text layer rather than paying
// the VLM. The threshold is deliberately low and must stay that way.
func TestARealTextLayerStillCountsAsContent(t *testing.T) {
	const caption = "Figure 3 — the disputed 25-foot strip, looking north from West View Boulevard."
	if got := textLayerContent(caption); got < pdfTextThreshold {
		t.Errorf("content = %d, threshold %d — a real caption would be sent for OCR", got, pdfTextThreshold)
	}
}
