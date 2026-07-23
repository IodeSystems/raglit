package raglit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	textExts = map[string]bool{".txt": true, ".md": true, ".markdown": true, ".text": true}
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
	case imageExts[ext] || strings.HasPrefix(ct, "image/"):
		return KindImage
	case officeExts[ext]:
		return KindOffice
	case textExts[ext] || strings.HasPrefix(ct, "text/plain") || strings.HasPrefix(ct, "text/markdown"):
		return KindText
	case strings.Contains(ct, "officedocument"), strings.Contains(ct, "opendocument"),
		strings.Contains(ct, "epub"), strings.Contains(ct, "rtf"), strings.Contains(ct, "text/html"):
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

// pdfTextThreshold: a page's pdftotext output must carry at least this many
// non-space characters to count as a real text layer; below it the page is
// treated as scanned and rasterized for OCR. Low, so a page with even a caption
// keeps its (cheap, exact) text layer rather than paying the VLM.
const pdfTextThreshold = 24

// pdfUnits extracts a PDF as per-page ingest units via the "text-layer first,
// OCR the rest" hybrid: pdftotext gives each page's text layer; a page with real
// text becomes a text unit (free, exact — no VLM), a page without (scanned) is
// rasterized with pdftoppm into an image unit for the OCR path. Born-digital PDFs
// are all text units; scanned PDFs all image units; mixed PDFs a blend.
//
// Without poppler it falls back to embedded-image extraction (Pagify), which
// cannot see a text layer — so born-digital PDFs then still fail (ErrNoPageImages).
func pdfUnits(ctx context.Context, pdfPath string) ([]ingestUnit, error) {
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
	units := make([]ingestUnit, 0, len(texts))
	for i, t := range texts {
		page := i + 1
		if len(strings.TrimSpace(t)) >= pdfTextThreshold {
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
		units, err := pdfUnits(ctx, path)
		if err != nil {
			return nil, err
		}
		return unitsToPageText(ctx, units, ocr)
	case KindOffice:
		text, err := PandocText(ctx, path)
		if err != nil {
			return nil, err
		}
		return []PageText{{Page: 1, Text: strings.TrimSpace(text), Engine: "text"}}, nil
	case KindImage:
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return unitsToPageText(ctx, []ingestUnit{{page: 1, mime: mimeForExt(filepath.Ext(path)), data: data}}, ocr)
	default: // text / unknown
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return []PageText{{Page: 1, Text: strings.TrimSpace(string(data)), Engine: "text"}}, nil
	}
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
