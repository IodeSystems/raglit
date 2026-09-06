package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

func TestResolveDoc(t *testing.T) {
	// data path: base64 PNG → temp file with a .png extension (from mime).
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	fp, cleanup, err := resolveDoc("", base64.StdEncoding.EncodeToString(png), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fp, ".png") {
		t.Errorf("temp path %q should end .png (mime-inferred)", fp)
	}
	if b, _ := os.ReadFile(fp); len(b) != len(png) {
		t.Errorf("temp file didn't get the bytes")
	}
	cleanup()
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Errorf("cleanup should have removed the temp file")
	}

	// path path: file:// prefix stripped, used as-is (no temp).
	fp2, cl2, err := resolveDoc("file:///etc/hostname", "", "")
	cl2()
	if err != nil || fp2 != "/etc/hostname" {
		t.Errorf("path resolve = %q, %v; want /etc/hostname", fp2, err)
	}

	// errors: both, neither, bad base64.
	if _, _, err := resolveDoc("a", "b", ""); err == nil {
		t.Error("both path+data should error")
	}
	if _, _, err := resolveDoc("", "", ""); err == nil {
		t.Error("neither should error")
	}
	if _, _, err := resolveDoc("", "!!bad!!", ""); err == nil {
		t.Error("bad base64 should error")
	}
}

// ocrDocument over a plain-text file: routed to the text path, one page, engine
// "text", no OCR needed.
func TestOCRDocument_Text(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(fp, []byte("  hello raglit  "), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := ocrDocument(context.Background(), &raglit.OCR{}, fp)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Pages) != 1 || out.Pages[0].Text != "hello raglit" || out.Pages[0].Engine != "text" {
		t.Errorf("pages = %+v", out.Pages)
	}
	if out.Engines["text"] != 1 {
		t.Errorf("engine tally = %v", out.Engines)
	}
}
