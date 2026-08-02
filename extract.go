package raglit

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DocKind is how a source document should be extracted to text. raglit routes
// each ingested file to the cheapest extractor that fits its kind — the "cascade
// all the way down" idea: a PDF page uses its text layer if it has one, OCR if
// not; office/markup goes through pandoc; images go through OCR; text is read.
type DocKind int

const (
	KindText   DocKind = iota // .txt/.md/… or text/* → read + segment
	KindPDF                   // .pdf → per-page hybrid (text layer or OCR)
	KindImage                 // .png/.jpg/… → OCR
	KindOffice                // .docx/.odt/.epub/.html/… → pandoc → text
	// KindEmail is an .eml archive: one PAGE per nested message, headers kept.
	// Not KindText — read as plain text an email archive is mostly base64, and
	// read as one page a quotation from the fifth message can only be located
	// somewhere in the whole file.
	KindEmail
	// KindSpreadsheet is .xlsx/.xls: one PAGE per sheet, same reasoning as
	// KindEmail — a fact on the "Q3 Budget" tab must be locatable to THAT tab,
	// not buried in a single page mixing every sheet in the workbook. Not
	// KindOffice: pandoc has no xlsx/xls reader at all (its own
	// --list-input-formats omits both — csv is the only spreadsheet format it
	// takes), and even if it did, OfficeText's one-flat-string-per-document
	// shape is the wrong output for a multi-sheet source.
	KindSpreadsheet
	KindUnknown
)

// String names a kind stably, for the pool key.
//
// These strings are part of a CACHE KEY, so they are written out by hand rather
// than derived from the constant's position: renumbering the iota, or inserting
// a kind in the middle, must not silently re-key every cached document. Adding a
// new name here is free; changing an existing one invalidates that kind's cache,
// which is the correct behaviour if the routing itself changed and a mistake
// otherwise.
func (k DocKind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindPDF:
		return "pdf"
	case KindImage:
		return "image"
	case KindOffice:
		return "office"
	case KindEmail:
		return "email"
	case KindSpreadsheet:
		return "spreadsheet"
	}
	return "unknown"
}

var (
	officeExts = map[string]bool{
		".docx": true, ".odt": true, ".epub": true, ".html": true, ".htm": true,
		".pptx": true, ".rtf": true, ".tex": true, ".latex": true, ".org": true,
		".rst": true, ".textile": true, ".mediawiki": true, ".docbook": true, ".fb2": true,
	}
	imageExts = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true,
		".webp": true, ".gif": true, ".bmp": true,
	}
	// heicExts are Apple's HEIC/HEIF photos — every iPhone photo since iOS 11.
	// Deliberately NOT in imageExts: the OCR path below (and the OCR/vision HTTP
	// clients further downstream) reads raw bytes and calls it image/<ext>. None
	// of tesseract (leptonica), the paddleocr sidecar, or a vision-model API
	// accept image/heic — so unlike every other image extension, a HEIC file must
	// be transcoded before it reaches that path, not just relabeled.
	heicExts = map[string]bool{".heic": true, ".heif": true}
	// textExts includes .csv: it is already delimited plain text and the
	// deterministic text fragmenter handles it fine as-is. Before this it
	// classified KindUnknown, which IS still readable as text (the KindUnknown
	// fallback), but expandIngestTargets skips anything ClassifyDoc calls
	// KindUnknown — so a directory of .csv files silently enqueued nothing.
	textExts = map[string]bool{".txt": true, ".md": true, ".markdown": true, ".text": true, ".csv": true}
	// legacyDocExts are pre-2007 binary Word documents. Deliberately NOT in
	// officeExts: pandoc cannot read them — the format is an OLE2 compound file,
	// not the zipped XML of .docx — and routing one there fails with a parse
	// error that reads like a corrupt file rather than an unsupported format.
	// Law firms still send .doc; the engagement letter in ardley-v-brannock is one.
	legacyDocExts = map[string]bool{".doc": true}
	// xlsxExts/xlsExts are split the same way officeExts/legacyDocExts are: same
	// KindSpreadsheet, different extractor. .xlsx is zipped XML, read natively
	// (xlsxPages, stdlib only — no external tool exists for it here: pandoc
	// doesn't read it, and neither libreoffice/soffice nor a python
	// openpyxl/pandas stack is installed on the reference machine). .xls is the
	// pre-2007 OLE2/BIFF binary format, same container family as legacy .doc but
	// a different internal structure antiword cannot read — xls2csv (part of the
	// same catdoc package as antiword's fallback) is the one tool on this
	// machine that reads it.
	xlsxExts = map[string]bool{".xlsx": true}
	xlsExts  = map[string]bool{".xls": true}
)

// ClassifyDoc routes a source by extension first, then content-type. Extension
// wins because it is the most reliable signal for a local file; content-type
// covers extensionless HTTP fetches.
func ClassifyDoc(name, contentType string) DocKind {
	ext := strings.ToLower(filepath.Ext(name))
	ct := strings.ToLower(contentType)
	switch {
	case ext == ".pdf" || strings.Contains(ct, "application/pdf"):
		return KindPDF
	case imageExts[ext] || heicExts[ext] || strings.HasPrefix(ct, "image/"):
		return KindImage
	case officeExts[ext] || legacyDocExts[ext]:
		return KindOffice
	case xlsxExts[ext] || xlsExts[ext] || strings.Contains(ct, "spreadsheetml") || strings.Contains(ct, "ms-excel"):
		return KindSpreadsheet
	case ext == ".eml" || ext == ".mbox" || strings.HasPrefix(ct, "message/rfc822"):
		return KindEmail
	case textExts[ext] || strings.HasPrefix(ct, "text/plain") || strings.HasPrefix(ct, "text/markdown"):
		return KindText
	case strings.Contains(ct, "officedocument"), strings.Contains(ct, "opendocument"),
		strings.Contains(ct, "epub"), strings.Contains(ct, "rtf"), strings.Contains(ct, "text/html"),
		strings.Contains(ct, "application/msword"):
		return KindOffice
	}
	return KindUnknown
}

// fileMagic maps a leading byte signature to what the file actually is.
//
// ZIP (PK\x03\x04) is deliberately absent. .docx, .xlsx, .odt and .epub are all
// zips, and so is a plain archive of unrelated files; routing on the container
// alone would send a backup zip to pandoc. Deciding between them means reading
// the archive's member names, which is a different and heavier question than
// "what are the first few bytes", and it is not the failure seen here.
var fileMagic = []struct {
	prefix []byte
	kind   DocKind
}{
	{[]byte("%PDF-"), KindPDF},
	{[]byte("\x89PNG\r\n\x1a\n"), KindImage},
	{[]byte{0xFF, 0xD8, 0xFF}, KindImage}, // JPEG
	{[]byte("GIF87a"), KindImage},
	{[]byte("GIF89a"), KindImage},
	{[]byte{0x49, 0x49, 0x2A, 0x00}, KindImage}, // TIFF, little-endian
	{[]byte{0x4D, 0x4D, 0x00, 0x2A}, KindImage}, // TIFF, big-endian
	{ole2Magic[:], KindOffice},
}

// SniffKind reads a file's leading bytes and says what it is, ignoring its name.
//
// ClassifyDoc routes on the extension because for a local file the extension is
// the most reliable signal available — but it is only a signal, and a file that
// has NO extension gets KindUnknown, which the worker reads as plain text.
//
// For a PDF that is not a near-miss, it is garbage: the index fills with
// `%PDF-1.7`, `obj` and `endobj` while the document's actual text stays
// unsearchable, and nothing reports an error because reading bytes as text
// always "works". Two attachments in ardley-v-brannock arrived exactly this way,
// their names truncated at 102 characters by the tool that unpacked them, losing
// the `.pdf` — the same class of defect the email extractor's own fix names
// ("an unnamed attachment kept no extension, so nothing indexed it").
//
// Returns KindUnknown when nothing matches, so the caller keeps its existing
// behaviour and this can only ever add routing, never redirect a file that the
// extension already classified.
func SniffKind(path string) DocKind {
	f, err := os.Open(path)
	if err != nil {
		return KindUnknown
	}
	defer f.Close()
	var buf [8]byte
	n, _ := io.ReadFull(f, buf[:])
	return SniffBytes(buf[:n])
}

// SniffBytes is SniffKind for content already in hand — the ingest path holds
// the bytes and never the file, so a path-only sniff would not reach it.
func SniffBytes(head []byte) DocKind {
	for _, m := range fileMagic {
		if len(head) >= len(m.prefix) && bytes.Equal(head[:len(m.prefix)], m.prefix) {
			return m.kind
		}
	}
	return KindUnknown
}

// ClassifyPath is ClassifyDoc for a file that exists on disk: the extension
// decides, and only when it decides nothing does the content get a say.
//
// The order matters and is not the obvious one. Sniffing FIRST would be more
// "correct" in the abstract and worse in practice — a .docx and a .odt are both
// zips, a .md and a .csv are both text, and the extension separates them where
// the bytes do not. The bytes are the fallback, not the authority.
func ClassifyPath(path, contentType string) DocKind {
	if k := ClassifyDoc(path, contentType); k != KindUnknown {
		return k
	}
	return SniffKind(path)
}

// toolPath returns a tool's resolved path (empty if not on PATH).
func toolPath(bin string) string { p, _ := exec.LookPath(bin); return p }

// HavePoppler / HavePandoc report whether the external extractors are available,
// so callers (and `raglit doctor`) can degrade gracefully.
func HavePoppler() bool { return toolPath("pdftotext") != "" && toolPath("pdftoppm") != "" }
func HavePandoc() bool  { return toolPath("pandoc") != "" }

// legacyDocTools convert binary .doc, in preference order. antiword handles Word
// 6/7/8/97-2003 and preserves paragraph breaks, which matters because the text
// is about to be fragmented; catdoc is the fallback and flattens harder.
var legacyDocTools = []string{"antiword", "catdoc"}

func legacyDocTool() string {
	for _, b := range legacyDocTools {
		if p := toolPath(b); p != "" {
			return p
		}
	}
	return ""
}

// HaveLegacyDoc reports whether a binary .doc converter is on PATH.
func HaveLegacyDoc() bool { return legacyDocTool() != "" }

// heicTools convert HEIC/HEIF, in preference order. Both are ImageMagick — v7's
// `magick` first, v6's `convert` as a fallback for a machine that only has the
// older package. Neither vips nor heif-convert is assumed installed.
//
// Both builds can only READ heic/heif (`magick -list format` reports `HEIC r--`,
// `HEIF r--` — no encode delegate). Verified on this machine: asking either to
// WRITE a .heic does not error out cleanly, it silently writes a plain PNG under
// the .heic name and exits 0. That is only a hazard for code that tries to
// PRODUCE heic; HEICToPNG below only ever reads one, so it does not hit it — but
// it means neither tool is a candidate for round-tripping a test fixture, only
// for decoding one.
var heicTools = []string{"magick", "convert"}

func heicTool() string {
	for _, b := range heicTools {
		if p := toolPath(b); p != "" {
			return p
		}
	}
	return ""
}

// HaveHEIC reports whether a HEIC/HEIF → PNG converter is on PATH.
func HaveHEIC() bool { return heicTool() != "" }

// HEICToPNG decodes a HEIC/HEIF photo to PNG bytes for the OCR path. PNG, not
// JPEG: these are photographed surveys, plats, permits and correspondence headed
// straight for OCR, and a second lossy generation ahead of OCR costs accuracy on
// exactly the small print that matters — the first (HEIC's own) generation is
// unavoidable, a second is not.
//
// -auto-orient matters and is not a no-op to skip: a phone's sensor is
// landscape, and a portrait shot only reads right-side-up because of a rotation
// signal carried alongside the pixels — an OCR pass over the raw sensor
// orientation reads sideways text and transcribes it to nothing. HEIF carries
// that signal two ways: a container-level irot/imir transform (the MIAF-compliant
// mechanism nearly all camera-generated HEIC actually uses) or, occasionally, a
// bare Exif Orientation tag as compatibility metadata. -auto-orient applies
// either; ImageMagick's HEIC decoder historically only honored the container
// form (github.com/ImageMagick/ImageMagick issue #1232, fixed by commit
// ba470aad in the version this machine has), which is the form real camera
// output actually carries, so this is not a synthetic concern.
func HEICToPNG(ctx context.Context, path string) ([]byte, error) {
	tool := heicTool()
	if tool == "" {
		return nil, fmt.Errorf("no HEIC/HEIF converter for %s (install imagemagick — `magick` or `convert`) — `raglit doctor` has the install hint", filepath.Ext(path))
	}
	out, err := exec.CommandContext(ctx, tool, path, "-auto-orient", "png:-").Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", filepath.Base(tool), filepath.Ext(path), err)
	}
	return out, nil
}
func pdfUnits(ctx context.Context, pdfPath string, canOCR bool) ([]ingestUnit, error) {
	if !HavePoppler() {
		pages, err := Pagify(pdfPath, "")
		if err != nil {
			return nil, err
		}
		units := make([]ingestUnit, 0, len(pages))
		for _, p := range pages {
			units = append(units, ingestUnit{page: p.Page, mime: p.Mime, data: p.Data})
		}
		return units, nil
	}
	if !canOCR {
		// Nothing can read the pages. Take the text layer, because the
		// alternative is an empty document — and it is the caller's job to have
		// configured a model if that is not good enough.
		texts, err := pdftotextPages(ctx, pdfPath)
		if err != nil {
			return nil, err
		}
		units := make([]ingestUnit, 0, len(texts))
		for i, t := range texts {
			units = append(units, ingestUnit{page: i + 1, text: t})
		}
		return units, nil
	}
	n, err := pdfPageCount(ctx, pdfPath)
	if err != nil {
		return nil, err
	}
	units := make([]ingestUnit, 0, n)
	for page := 1; page <= n; page++ {
		img, mime, err := pdftoppmPage(ctx, pdfPath, page)
		if err != nil {
			return nil, err
		}
		units = append(units, ingestUnit{page: page, mime: mime, data: img})
	}
	return units, nil
}

// pdfPageCount asks poppler how many pages a PDF has.
func pdfPageCount(ctx context.Context, pdfPath string) (int, error) {
	out, err := exec.CommandContext(ctx, "pdfinfo", pdfPath).Output()
	if err != nil {
		return 0, fmt.Errorf("pdfinfo: %w", err)
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(ln, "Pages:") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(ln, "Pages:")))
		if err != nil {
			return 0, fmt.Errorf("pdfinfo: unreadable page count %q", ln)
		}
		return n, nil
	}
	return 0, fmt.Errorf("pdfinfo: no page count for %s", pdfPath)
}
func pdftotextPages(ctx context.Context, pdfPath string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "pdftotext", "-layout", pdfPath, "-").Output()
	if err != nil {
		return nil, fmt.Errorf("pdftotext: %w", err)
	}
	pages := strings.Split(string(out), "\f")
	if n := len(pages); n > 0 && strings.TrimSpace(pages[n-1]) == "" {
		pages = pages[:n-1] // drop the trailing form feed's empty tail
	}
	return pages, nil
}

// pdftoppmPage renders one PDF page to a PNG (200 DPI) for OCR.
func pdftoppmPage(ctx context.Context, pdfPath string, page int) ([]byte, string, error) {
	dir, err := os.MkdirTemp("", "raglit-ppm-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "p")
	ps := fmt.Sprintf("%d", page)
	cmd := exec.CommandContext(ctx, "pdftoppm", "-png", "-r", "200", "-f", ps, "-l", ps, "-singlefile", pdfPath, prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("pdftoppm p%d: %w (%s)", page, err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(prefix + ".png")
	if err != nil {
		return nil, "", err
	}
	return data, "image/png", nil
}

// PageText is one page's extracted text plus the engine that produced it, for
// the `ocr` MCP tool's paged output. Engine is "text" for a PDF text layer,
// pandoc, or a plain-text read; "tesseract"/"paddleocr"/"vision" for an OCR'd
// (scanned/image) page.
type PageText struct {
	Page   int
	Text   string
	Engine string
}

// ExtractPaged extracts a document to paged text — the `ocr` MCP tool's core.
// It routes by kind: a PDF runs the text-layer/OCR hybrid, office/markup goes
// through pandoc (one page), an image runs the OCR cascade, and text is read
// directly. ocr may be nil when no page needs OCR (a born-digital PDF, office, or
// text); a scanned page with a nil ocr is a clear error.
func ExtractPaged(ctx context.Context, path string, ocr *OCR) ([]PageText, error) {
	switch ClassifyDoc(path, "") {
	case KindPDF:
		units, err := pdfUnits(ctx, path, ocr != nil)
		if err != nil {
			return nil, err
		}
		return unitsToPageText(ctx, units, ocr)
	case KindEmail:
		return EmailText(path)
	case KindSpreadsheet:
		return SpreadsheetPages(ctx, path)
	case KindOffice:
		text, err := OfficeText(ctx, path)
		if err != nil {
			return nil, err
		}
		return []PageText{{Page: 1, Text: strings.TrimSpace(text), Engine: "text"}}, nil
	case KindImage:
		mime, data, err := imageUnitBytes(ctx, path)
		if err != nil {
			return nil, err
		}
		return unitsToPageText(ctx, []ingestUnit{{page: 1, mime: mime, data: data}}, ocr)
	default: // text / unknown
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return []PageText{{Page: 1, Text: strings.TrimSpace(string(data)), Engine: "text"}}, nil
	}
}

// imageUnitBytes reads an image file as an OCR-ready (mime, data) pair. A
// HEIC/HEIF is transcoded to PNG first (see HEICToPNG); every other image
// extension is read as-is and labeled by its extension — the OCR/vision path
// already accepts those formats directly, so converting them too would just be
// a second lossy generation for no reason.
func imageUnitBytes(ctx context.Context, path string) (mime string, data []byte, err error) {
	if heicExts[strings.ToLower(filepath.Ext(path))] {
		data, err = HEICToPNG(ctx, path)
		return "image/png", data, err
	}
	data, err = os.ReadFile(path)
	return mimeForExt(filepath.Ext(path)), data, err
}

// unitsToPageText turns ingest units into paged text: text units pass through
// (engine "text"); image units run the OCR cascade (engine per its result).
func unitsToPageText(ctx context.Context, units []ingestUnit, ocr *OCR) ([]PageText, error) {
	out := make([]PageText, 0, len(units))
	for _, u := range units {
		if !u.isImage() {
			out = append(out, PageText{Page: u.page, Text: strings.TrimSpace(u.text), Engine: "text"})
			continue
		}
		if ocr == nil {
			return nil, fmt.Errorf("page %d is a scanned image but no OCR/vision model is configured", u.page)
		}
		text, engine, err := ocr.PageWithEngine(ctx, PageImage{Page: u.page, Mime: u.mime, Data: u.data})
		if err != nil {
			return nil, err
		}
		out = append(out, PageText{Page: u.page, Text: text, Engine: engine})
	}
	return out, nil
}

// ExtForContentType maps a content type to a file extension for materializing
// fetched/base64 bytes to a temp file (external tools detect format by
// extension). Empty when unknown — the caller should sniff or default.
func ExtForContentType(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch {
	case mime == "application/pdf":
		return ".pdf"
	case mime == "image/png":
		return ".png"
	case mime == "image/jpeg":
		return ".jpg"
	case mime == "image/tiff":
		return ".tif"
	case mime == "image/webp":
		return ".webp"
	case mime == "image/gif":
		return ".gif"
	case strings.Contains(mime, "wordprocessingml"):
		return ".docx"
	case strings.Contains(mime, "presentationml"):
		return ".pptx"
	case strings.Contains(mime, "opendocument.text"):
		return ".odt"
	case strings.Contains(mime, "epub"):
		return ".epub"
	case mime == "text/html":
		return ".html"
	case mime == "application/rtf", mime == "text/rtf":
		return ".rtf"
	case strings.HasPrefix(mime, "text/"):
		return ".txt"
	}
	return ""
}

// PandocText converts an office/markup document (docx, odt, epub, html, pptx, …)
// to plain text via pandoc, which auto-detects the input format from the file
// extension. The path must have the right extension.
func PandocText(ctx context.Context, path string) (string, error) {
	if !HavePandoc() {
		return "", fmt.Errorf("pandoc not installed (needed for %s files) — `raglit doctor` has the install hint", filepath.Ext(path))
	}
	out, err := exec.CommandContext(ctx, "pandoc", path, "-t", "plain", "-o", "-").Output()
	if err != nil {
		return "", fmt.Errorf("pandoc %s: %w", filepath.Ext(path), err)
	}
	return string(out), nil
}

// LegacyDocText converts a pre-2007 binary Word .doc to plain text.
//
// Why not LibreOffice. `libreoffice --headless --convert-to docx` is the
// obvious answer and it is the wrong one here: it is a ~1GB dependency to read a
// format that is almost always a letter, it costs seconds per file against
// milliseconds, and it serialises on a single user-profile lock — so raglit's
// concurrent workers would either collide or need a throwaway
// -env:UserInstallation per job. antiword is ~200KB and does nothing else.
//
// The trade it makes: antiword drops embedded images, so a .doc whose content is
// a scanned figure extracts as empty. That is the right trade for correspondence
// and agreements; if a .doc ever matters for its figures, convert it to PDF and
// let the PDF path rasterize and describe them.
func LegacyDocText(ctx context.Context, path string) (string, error) {
	tool := legacyDocTool()
	if tool == "" {
		return "", fmt.Errorf("no converter for legacy %s files (install antiword, or catdoc) — `raglit doctor` has the install hint", filepath.Ext(path))
	}
	out, err := exec.CommandContext(ctx, tool, path).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", filepath.Base(tool), filepath.Ext(path), err)
	}
	return string(out), nil
}

// ole2Magic is the first eight bytes of an OLE2 compound file — the container
// format of every pre-2007 binary Office document (.doc, .xls, .ppt). People
// rename a .doc to .docx by hand hoping it will "just open" in something that
// only reads .docx; the extension then lies about the format underneath.
var ole2Magic = [8]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// isOLE2 sniffs the header, ignoring the extension. It exists because pandoc
// fails on a mislabeled OLE2 file with a parse error that reads like the file is
// corrupt, when what actually happened is knowable in eight bytes and cheap
// enough to check on every KindOffice file, not just ones already named .doc.
func isOLE2(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var buf [8]byte
	if _, err := io.ReadFull(f, buf[:]); err != nil {
		return false
	}
	return bytes.Equal(buf[:], ole2Magic[:])
}

// OfficeText extracts text from any KindOffice document, choosing the converter
// by extension — except an OLE2 header overrides the extension, because a
// renamed .doc is still a .doc underneath and pandoc still cannot read it.
// Callers route KindOffice here rather than to PandocText directly, so adding a
// format that pandoc cannot read is a change in one place.
func OfficeText(ctx context.Context, path string) (string, error) {
	if legacyDocExts[strings.ToLower(filepath.Ext(path))] || isOLE2(path) {
		return LegacyDocText(ctx, path)
	}
	return PandocText(ctx, path)
}

// HaveXLS reports whether xls2csv (the legacy-.xls converter, part of the same
// catdoc package as antiword's fallback) is on PATH.
func HaveXLS() bool { return toolPath("xls2csv") != "" }

// SpreadsheetPages extracts a workbook to one PageText per sheet. .xlsx is read
// natively (no external tool exists for it — see xlsxExts); .xls goes through
// xls2csv.
func SpreadsheetPages(ctx context.Context, path string) ([]PageText, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx":
		return xlsxPages(path)
	case ".xls":
		return xlsPages(ctx, path)
	}
	return nil, fmt.Errorf("not a spreadsheet: %s", path)
}

// xlsPages shells out to xls2csv, which separates sheets with a form feed by
// default (`-b`, undocumented default is exactly the char pdftotext also uses
// for page breaks) — so the split is the same one-liner as pdftotextPages.
func xlsPages(ctx context.Context, path string) ([]PageText, error) {
	if !HaveXLS() {
		return nil, fmt.Errorf("no converter for legacy .xls files (install catdoc, which provides xls2csv) — `raglit doctor` has the install hint")
	}
	out, err := exec.CommandContext(ctx, "xls2csv", path).Output()
	if err != nil {
		return nil, fmt.Errorf("xls2csv: %w", err)
	}
	sheets := strings.Split(string(out), "\f")
	if n := len(sheets); n > 0 && strings.TrimSpace(sheets[n-1]) == "" {
		sheets = sheets[:n-1] // drop the trailing sheet-break's empty tail
	}
	pages := make([]PageText, 0, len(sheets))
	for i, s := range sheets {
		pages = append(pages, PageText{Page: i + 1, Text: strings.TrimSpace(s), Engine: "text"})
	}
	return pages, nil
}

// --- .xlsx: read natively (archive/zip + encoding/xml, stdlib only) ---
//
// An .xlsx is a zip of small XML parts. Three of them matter for text:
// workbook.xml (sheet order + names + relationship ids), workbook.xml.rels
// (relationship id → sheet file path), and each sheetN.xml itself (the cells).
// sharedStrings.xml is a fourth, optional one — see xlSST below.

// xlWorkbook is xl/workbook.xml — gives sheet order, names, and each sheet's
// relationship id, which xl/_rels/workbook.xml.rels then resolves to a path.
type xlWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
}

// xlRelationships is xl/_rels/workbook.xml.rels.
type xlRelationships struct {
	Rel []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

// xlSST is xl/sharedStrings.xml. Excel itself dedupes strings here and cells
// reference them by index (t="s"); some writers (openpyxl in its default mode,
// observed while building this) skip the shared-string table entirely and
// inline every string on the cell (t="inlineStr") instead — sharedStrings.xml
// then doesn't exist at all, which is why loading it is tolerant of ENOENT.
type xlSST struct {
	SI []struct {
		T string `xml:"t"` // direct text: <si><t>Hello</t></si>
		R []struct {
			T string `xml:"t"`
		} `xml:"r"` // rich-text runs: <si><r><t>Hel</t></r><r><t>lo</t></r></si>
	} `xml:"si"`
}

func (s xlSST) text(i int) string {
	if i < 0 || i >= len(s.SI) {
		return ""
	}
	si := s.SI[i]
	if si.T != "" || len(si.R) == 0 {
		return si.T
	}
	var b strings.Builder
	for _, r := range si.R {
		b.WriteString(r.T)
	}
	return b.String()
}

// xlWorksheet is one xl/worksheets/sheetN.xml.
type xlWorksheet struct {
	SheetData struct {
		Row []struct {
			C []struct {
				Type string `xml:"t,attr"` // "s"=shared string, "inlineStr"=inline, else literal (number/bool/formula-result)
				V    string `xml:"v"`
				IS   struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
}

// zipReadFile returns one archive entry's bytes, or ok=false if it isn't
// present — callers decide whether that's an error (sharedStrings.xml is
// legitimately optional; workbook.xml is not).
func zipReadFile(zr *zip.Reader, name string) (data []byte, ok bool) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return nil, false
		}
		return b, true
	}
	return nil, false
}

// xlsxSheetPath resolves a workbook.xml.rels Target to a zip entry name.
// Targets are written either relative to xl/ (the common case: "worksheets/
// sheet1.xml") or as a package-absolute path ("/xl/worksheets/sheet1.xml",
// observed from openpyxl while building this) — both forms are legal OPC.
func xlsxSheetPath(target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	return "xl/" + target
}

// xlsxPages reads an .xlsx to one PageText per sheet, in workbook order, cells
// tab-joined within a row and rows newline-joined. It does not evaluate
// formulas (only the last-computed <v> a writer stored, if any), preserve
// number formatting, or read merged cells / charts / comments — the same trade
// antiword makes on embedded images: the right size of tool for making a
// spreadsheet's text searchable and citable, not for reproducing it.
func xlsxPages(path string) ([]PageText, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("xlsx: open: %w", err)
	}
	defer zr.Close()

	wbBytes, ok := zipReadFile(&zr.Reader, "xl/workbook.xml")
	if !ok {
		return nil, fmt.Errorf("xlsx: missing xl/workbook.xml")
	}
	var wb xlWorkbook
	if err := xml.Unmarshal(wbBytes, &wb); err != nil {
		return nil, fmt.Errorf("xlsx: parse workbook.xml: %w", err)
	}

	relBytes, ok := zipReadFile(&zr.Reader, "xl/_rels/workbook.xml.rels")
	if !ok {
		return nil, fmt.Errorf("xlsx: missing xl/_rels/workbook.xml.rels")
	}
	var rels xlRelationships
	if err := xml.Unmarshal(relBytes, &rels); err != nil {
		return nil, fmt.Errorf("xlsx: parse workbook.xml.rels: %w", err)
	}
	targetByRID := make(map[string]string, len(rels.Rel))
	for _, r := range rels.Rel {
		targetByRID[r.ID] = r.Target
	}

	var sst xlSST
	if sstBytes, ok := zipReadFile(&zr.Reader, "xl/sharedStrings.xml"); ok {
		if err := xml.Unmarshal(sstBytes, &sst); err != nil {
			return nil, fmt.Errorf("xlsx: parse sharedStrings.xml: %w", err)
		}
	}

	pages := make([]PageText, 0, len(wb.Sheets))
	for i, sh := range wb.Sheets {
		target, ok := targetByRID[sh.RID]
		if !ok {
			continue // a sheet with a dangling relationship id — skip rather than fail the whole workbook
		}
		sheetBytes, ok := zipReadFile(&zr.Reader, xlsxSheetPath(target))
		if !ok {
			continue
		}
		var ws xlWorksheet
		if err := xml.Unmarshal(sheetBytes, &ws); err != nil {
			return nil, fmt.Errorf("xlsx: parse sheet %q: %w", sh.Name, err)
		}
		var b strings.Builder
		for _, row := range ws.SheetData.Row {
			cells := make([]string, 0, len(row.C))
			for _, c := range row.C {
				switch c.Type {
				case "s":
					idx, err := strconv.Atoi(strings.TrimSpace(c.V))
					if err != nil {
						cells = append(cells, "")
						continue
					}
					cells = append(cells, sst.text(idx))
				case "inlineStr":
					cells = append(cells, c.IS.T)
				default:
					cells = append(cells, c.V)
				}
			}
			b.WriteString(strings.Join(cells, "\t"))
			b.WriteByte('\n')
		}
		title := sh.Name
		if title == "" {
			title = fmt.Sprintf("Sheet %d", i+1)
		}
		pages = append(pages, PageText{
			Page:   i + 1,
			Text:   fmt.Sprintf("## %s\n\n%s", title, strings.TrimSpace(b.String())),
			Engine: "text",
		})
	}
	return pages, nil
}
