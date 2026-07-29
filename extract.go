package raglit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
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
	KindUnknown
)

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
	textExts = map[string]bool{".txt": true, ".md": true, ".markdown": true, ".text": true}
	// legacyDocExts are pre-2007 binary Word documents. Deliberately NOT in
	// officeExts: pandoc cannot read them — the format is an OLE2 compound file,
	// not the zipped XML of .docx — and routing one there fails with a parse
	// error that reads like a corrupt file rather than an unsupported format.
	// Law firms still send .doc; the engagement letter in ardley-v-brannock is one.
	legacyDocExts = map[string]bool{".doc": true}
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

// pdfTextThreshold: a page's pdftotext output must carry at least this many
// LETTERS AND DIGITS to count as a real text layer; below it the page is treated
// as scanned and rasterized for OCR. Low, so a page with even a caption keeps its
// (cheap, exact) text layer rather than paying the VLM.
const pdfTextThreshold = 24

// textLayerContent counts the characters that are actually CONTENT.
//
// The obvious `len(strings.TrimSpace(t))` is wrong, and wrong in a way that
// produced the worst corruption in a live legal corpus. `pdftotext -layout` pads
// with spaces to preserve position on the page, so a diagonal "UNOFFICIAL
// DOCUMENT" watermark — eighteen letters spread corner to corner — comes back as
// 144 characters. TrimSpace only trims the ENDS; the internal padding stays,
// sails past the threshold, and the page is accepted as a text layer that never
// goes near OCR.
//
// The result: a six-page summary-judgment order, a vesting deed and a record of
// survey all "transcribed" to nothing but their watermark, no error anywhere, and
// re-indexing them changed nothing because there was nothing to re-read — the
// text layer was being taken every time. Measured on all three: raw length 144,
// content 18.
func textLayerContent(t string) int {
	n := 0
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// pdfUnits extracts a PDF as per-page ingest units via the "text-layer first,
// OCR the rest" hybrid: pdftotext gives each page's text layer; a page with real
// text becomes a text unit (free, exact — no VLM), a page without (scanned) is
// rasterized with pdftoppm into an image unit for the OCR path. Born-digital PDFs
// are all text units; scanned PDFs all image units; mixed PDFs a blend.
//
// Without poppler it falls back to embedded-image extraction (Pagify), which
// cannot see a text layer — so born-digital PDFs then still fail (ErrNoPageImages).
func pdfUnits(ctx context.Context, pdfPath string, describeFigures bool) ([]ingestUnit, error) {
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
	texts, err := pdftotextPages(ctx, pdfPath)
	if err != nil {
		return nil, err
	}
	// Figure gate (§3a, opt-in): a text-layer page that also carries an embedded
	// image is rasterized to the VLM so its figures get described, even though its
	// text is clean. Detection is pdfcpu's embedded-image list (born-digital only).
	var figurePages map[int]bool
	if describeFigures {
		figurePages, _ = pagesWithImages(pdfPath) // best-effort; nil on error → no escalation
	}
	units := make([]ingestUnit, 0, len(texts))
	for i, t := range texts {
		page := i + 1
		if textLayerContent(t) >= pdfTextThreshold && !figurePages[page] {
			units = append(units, ingestUnit{page: page, text: t})
			continue
		}
		img, mime, err := pdftoppmPage(ctx, pdfPath, page)
		if err != nil {
			return nil, err
		}
		units = append(units, ingestUnit{page: page, mime: mime, data: img})
	}
	return units, nil
}

// pdftotextPages returns each page's text layer in order (pdftotext separates
// pages with a form feed).
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
		units, err := pdfUnits(ctx, path, ocr != nil && ocr.DescribeFigures)
		if err != nil {
			return nil, err
		}
		return unitsToPageText(ctx, units, ocr)
	case KindEmail:
		return EmailText(path)
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
