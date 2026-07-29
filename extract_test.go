package raglit

import (
	"archive/zip"
	"bytes"
	"context"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		{"photo.heic", "", KindImage},
		{"photo.HEIC", "", KindImage}, // extension match is case-insensitive
		{"photo.heif", "", KindImage},
		{"x", "image/heic", KindImage},
		{"paper.docx", "", KindOffice},
		{"book.epub", "", KindOffice},
		{"page.html", "", KindOffice},
		{"x", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", KindOffice},
		{"budget.xlsx", "", KindSpreadsheet},
		{"budget.XLSX", "", KindSpreadsheet},
		{"legacy.xls", "", KindSpreadsheet},
		{"x", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", KindSpreadsheet},
		{"x", "application/vnd.ms-excel", KindSpreadsheet},
		{"data.csv", "", KindText},
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

// A file named .docx that is really an OLE2 .doc (someone renamed it by hand,
// hoping it would "just convert") must sniff as OLE2 regardless of its
// extension, and a genuine zipped-XML .docx must not false-positive.
func TestOLE2NamedDocxRoutesToLegacyDoc(t *testing.T) {
	dir := t.TempDir()

	renamed := filepath.Join(dir, "engagement-letter.docx")
	content := append(append([]byte{}, ole2Magic[:]...), []byte("not real word-doc bytes, only the header matters")...)
	if err := os.WriteFile(renamed, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if !isOLE2(renamed) {
		t.Error("isOLE2: want true for a file starting with the OLE2 magic, regardless of its .docx name")
	}

	real := filepath.Join(dir, "real.docx")
	if err := os.WriteFile(real, []byte("PK\x03\x04 this is what zipped XML actually starts with"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isOLE2(real) {
		t.Error("isOLE2: want false for a real (zipped XML) .docx — false-positiving here would send it to antiword instead of pandoc")
	}

	// The routing decision itself: OfficeText must choose LegacyDocText for the
	// renamed file even though ClassifyDoc put it in KindOffice via its .docx
	// extension (mirrors TestLegacyDocRoutesAwayFromPandoc's error-message check).
	if !HaveLegacyDoc() {
		if _, err := OfficeText(context.Background(), renamed); err == nil || !strings.Contains(err.Error(), "antiword") {
			t.Errorf("OfficeText(renamed OLE2 .docx) = %v, want an error naming antiword (not a pandoc parse failure)", err)
		}
	}
}

// pillow_heif can WRITE a HEIC; ImageMagick (both `magick` and `convert` on this
// machine) can only READ one — `magick -list format` reports `HEIC r--`, and
// asking either to write a .heic silently produces a plain PNG under that name
// instead of erroring. So the test fixture is generated with python3+pillow_heif,
// not with the same tool under test, and skips cleanly when that generator is
// absent — this is a fixture-generation constraint, not a claim about what
// HEICToPNG itself depends on (it only ever reads).
func skipUnlessHEICFixturesPossible(t *testing.T) {
	t.Helper()
	if !HaveHEIC() {
		t.Skip("no HEIC/HEIF converter on PATH (magick/convert) — install imagemagick")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH — needed to generate a HEIC test fixture (ImageMagick can't write one)")
	}
	if err := exec.Command("python3", "-c", "import pillow_heif").Run(); err != nil {
		t.Skip("python3 pillow_heif not installed — needed to generate a HEIC test fixture (`pip install pillow-heif`)")
	}
}

// writeHEICFixture writes a small real HEIC photo to dir/name via python3
// pillow_heif — a high-contrast rectangle on white, at a known size, so the
// round trip can assert the converted PNG actually carries that content through
// rather than just existing.
func writeHEICFixture(t *testing.T, path string, w, h int) {
	t.Helper()
	script := `
import sys
import pillow_heif
from PIL import Image, ImageDraw
w, h, path = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3]
im = Image.new("RGB", (w, h), "white")
ImageDraw.Draw(im).rectangle([0, 0, w // 4, h // 4], fill="black")
pillow_heif.from_pillow(im).save(path, quality=90)
`
	cmd := exec.Command("python3", "-c", script, strconv.Itoa(w), strconv.Itoa(h), path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating HEIC fixture: %v: %s", err, out)
	}
}

// End-to-end: a real HEIC in, a decodable PNG with the right pixel dimensions
// out. This is worth more than a ClassifyDoc-only test because the thing that
// can actually break — the external `magick`/`convert` invocation, the "png:-"
// stdout format spec, the -auto-orient flag being accepted at all — only shows
// up when a real converter runs.
//
// It does NOT assert that a rotation was applied. Doing that honestly needs a
// HEIC carrying a container-level irot/imir transform (the form real camera
// output uses — see the HEICToPNG doc comment); pillow_heif's Python API here
// only exposes a way to attach an Exif Orientation tag, which is a DIFFERENT,
// MIAF-noncompliant signal that ImageMagick's HEIC decoder correctly ignores
// (confirmed empirically: -auto-orient left an Exif-orientation-6 fixture at its
// original 200x120, while the same tag on an otherwise-identical JPEG correctly
// flipped it to 120x200). Fabricating a real irot box means hand-editing the
// HEIF/ISOBMFF container, which is out of scope here; asserting a rotation this
// test cannot actually exercise would be exactly the "loosen a fixture" this
// codebase forbids, so the assertion is left out rather than faked.
func TestHEICToPNGEndToEnd(t *testing.T) {
	skipUnlessHEICFixturesPossible(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "photo.heic")
	const wantW, wantH = 200, 120
	writeHEICFixture(t, src, wantW, wantH)

	pngBytes, err := HEICToPNG(context.Background(), src)
	if err != nil {
		t.Fatalf("HEICToPNG: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("HEICToPNG output is not a decodable PNG: %v", err)
	}
	if cfg.Width != wantW || cfg.Height != wantH {
		t.Errorf("HEICToPNG size = %dx%d, want %dx%d", cfg.Width, cfg.Height, wantW, wantH)
	}

	// And the classify+ExtractPaged path routes a .heic through the same
	// conversion (this pins imageUnitBytes' branch, not just HEICToPNG alone).
	clean := PageOCR{Text: "converted heic transcribed fine", MeanConfidence: 0.95, BoxCount: 3}
	ocr := &OCR{Cheap: stubEngine{name: "tesseract", po: clean}}
	pages, err := ExtractPaged(context.Background(), src, ocr)
	if err != nil {
		t.Fatalf("ExtractPaged(.heic): %v", err)
	}
	if len(pages) != 1 || pages[0].Text != clean.Text {
		t.Errorf("pages = %+v", pages)
	}
}

// writeMinimalXLSX hand-builds a real, valid .xlsx with archive/zip + the
// stdlib only — no external tool can WRITE one on this machine either (same
// read-only situation as HEIC: no libreoffice/openpyxl/pandas is assumed
// installed), so unlike the HEIC fixture, this one is built in Go directly
// rather than shelled out to a generator.
//
// It deliberately exercises three things a real workbook can do that a lazier
// fixture would paper over: a shared-string cell (t="s", the form Excel itself
// writes), an inline-string cell (t="inlineStr", the form openpyxl was observed
// writing while this was built — sharedStrings.xml can legitimately not exist
// at all), and both a workbook.xml.rels Target form (relative "worksheets/
// sheet2.xml" for sheet1, package-absolute "/xl/worksheets/sheet2.xml" for
// sheet2 — both are legal OPC and real writers use either).
func writeMinimalXLSX(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	files := map[string]string{
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Budget" sheetId="1" r:id="rId1"/><sheet name="Notes" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/xl/worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2"><si><t>Line Item</t></si><si><t>Rent</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c></row><row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2"><v>1200</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>remember the escrow deadline</t></is></c></row></sheetData></worksheet>`,
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// The xlsx reader is pure stdlib (no external tool, no skip needed): shared
// strings resolve, inline strings resolve, both rels-Target forms resolve, and
// each sheet lands on its own page named after the sheet.
func TestXLSXPagesNative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "budget.xlsx")
	writeMinimalXLSX(t, path)

	pages, err := SpreadsheetPages(context.Background(), path)
	if err != nil {
		t.Fatalf("SpreadsheetPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2: %+v", len(pages), pages)
	}
	if pages[0].Page != 1 || pages[0].Text != "## Budget\n\nLine Item\nRent\t1200" {
		t.Errorf("page 1 = %+v", pages[0])
	}
	if pages[1].Page != 2 || pages[1].Text != "## Notes\n\nremember the escrow deadline" {
		t.Errorf("page 2 = %+v", pages[1])
	}
	// Routes through ExtractPaged/ClassifyDoc the same way, end to end.
	viaExtractPaged, err := ExtractPaged(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("ExtractPaged(.xlsx): %v", err)
	}
	if len(viaExtractPaged) != 2 {
		t.Errorf("ExtractPaged(.xlsx) = %d pages, want 2", len(viaExtractPaged))
	}
}

// xls2csv can WRITE a real legacy .xls (unlike HEIC/imagemagick, python3's
// xlwt can produce this binary format directly), so this end-to-end test is
// gated only on the tool availability, not a second missing-generator skip.
func skipUnlessXLSFixturesPossible(t *testing.T) {
	t.Helper()
	if !HaveXLS() {
		t.Skip("xls2csv not on PATH — install catdoc")
	}
	if err := exec.Command("python3", "-c", "import xlwt").Run(); err != nil {
		t.Skip("python3 xlwt not installed — needed to generate an .xls test fixture (`pip install xlwt`)")
	}
}

func writeXLSFixture(t *testing.T, path string) {
	t.Helper()
	script := `
import sys
import xlwt
wb = xlwt.Workbook()
s1 = wb.add_sheet("Budget")
s1.write(0, 0, "Line Item")
s1.write(1, 0, "Rent")
s1.write(1, 1, 1200)
s2 = wb.add_sheet("Notes")
s2.write(0, 0, "remember the escrow deadline")
wb.save(sys.argv[1])
`
	cmd := exec.Command("python3", "-c", script, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating .xls fixture: %v: %s", err, out)
	}
}

// End-to-end: a real legacy .xls in, one page per sheet out — xls2csv's
// default sheet-break is a form feed, split the same way pdftotextPages splits
// PDF pages, and this is worth pinning because the "-b" default is
// undocumented behavior discovered empirically, not something obvious from
// xls2csv's own --help text.
func TestXLSToPagesEndToEnd(t *testing.T) {
	skipUnlessXLSFixturesPossible(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "budget.xls")
	writeXLSFixture(t, src)

	pages, err := SpreadsheetPages(context.Background(), src)
	if err != nil {
		t.Fatalf("SpreadsheetPages(.xls): %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2 (one per sheet): %+v", len(pages), pages)
	}
	if !strings.Contains(pages[0].Text, "Line Item") || !strings.Contains(pages[0].Text, "Rent") {
		t.Errorf("page 1 missing expected content: %q", pages[0].Text)
	}
	if !strings.Contains(pages[1].Text, "escrow deadline") {
		t.Errorf("page 2 missing expected content: %q", pages[1].Text)
	}
}
