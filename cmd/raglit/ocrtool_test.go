package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit"
)

// fakeEngine is a cheap PageEngine returning a canned clean result, so
// ocrDocument's image path can run without tesseract or a real VLM.
type fakeEngine struct{ text string }

func (fakeEngine) Name() string { return "tesseract" }
func (e fakeEngine) OCRPage(context.Context, raglit.PageImage) (raglit.PageOCR, error) {
	return raglit.PageOCR{Text: e.text, MeanConfidence: 0.97, BoxCount: 6}, nil
}

func TestDocIsPDF(t *testing.T) {
	cases := []struct {
		raw        []byte
		mime, path string
		want       bool
	}{
		{[]byte("%PDF-1.7\n…"), "", "", true},                // magic
		{[]byte("\x89PNG\r\n"), "application/pdf", "", true}, // mime override
		{[]byte("\x89PNG\r\n"), "", "scan.PDF", true},        // extension (case-insensitive)
		{[]byte("\x89PNG\r\n"), "image/png", "x.png", false}, // a real image
		{[]byte("plain"), "", "", false},
	}
	for _, c := range cases {
		if got := docIsPDF(c.raw, c.mime, c.path); got != c.want {
			t.Errorf("docIsPDF(%q,%q) = %v, want %v", c.mime, c.path, got, c.want)
		}
	}
}

func TestLoadDoc(t *testing.T) {
	// base64 data path
	raw, mime, err := loadDoc("", base64.StdEncoding.EncodeToString([]byte("hello")), "image/png")
	if err != nil || string(raw) != "hello" || mime != "image/png" {
		t.Fatalf("data path: raw=%q mime=%q err=%v", raw, mime, err)
	}
	// file path via file:// with extension-derived mime
	dir := t.TempDir()
	fp := filepath.Join(dir, "page.jpg")
	if err := os.WriteFile(fp, []byte{0xff, 0xd8, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, mime, err = loadDoc("file://"+fp, "", "")
	if err != nil || len(raw) != 3 || mime != "image/jpeg" {
		t.Fatalf("file path: raw=%v mime=%q err=%v", raw, mime, err)
	}
	// both set → error; neither → error; bad base64 → error
	if _, _, err := loadDoc("x", "y", ""); err == nil {
		t.Error("both path+data should error")
	}
	if _, _, err := loadDoc("", "", ""); err == nil {
		t.Error("neither path nor data should error")
	}
	if _, _, err := loadDoc("", "!!not base64!!", ""); err == nil {
		t.Error("bad base64 should error")
	}
}

// ocrDocument's single-image path: a clean cheap result is trusted, one page out,
// engine tally reflects it, and the VLM is never touched (Client nil is fine).
func TestOCRDocument_SingleImage(t *testing.T) {
	ocr := &raglit.OCR{Cheap: fakeEngine{text: "the invoice total is 42 dollars"}}
	out, err := ocrDocument(context.Background(), ocr, []byte{0x89, 0x50, 0x4e, 0x47}, "image/png", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Pages) != 1 {
		t.Fatalf("want 1 page, got %d", len(out.Pages))
	}
	p := out.Pages[0]
	if p.Page != 1 || p.Engine != "tesseract" || p.Text != "the invoice total is 42 dollars" {
		t.Errorf("page = %+v", p)
	}
	if out.Engines["tesseract"] != 1 {
		t.Errorf("engine tally = %v, want tesseract:1", out.Engines)
	}
}

// A born-digital PDF (no embedded images) surfaces PagifyBytes's ErrNoPageImages
// rather than silently returning empty.
func TestOCRDocument_BornDigitalPDF(t *testing.T) {
	ocr := &raglit.OCR{Cheap: fakeEngine{text: "x"}}
	_, err := ocrDocument(context.Background(), ocr, []byte("%PDF-1.7\n%%EOF\n"), "application/pdf", "")
	if err == nil {
		t.Fatal("want an error for a PDF with no extractable page images")
	}
}
