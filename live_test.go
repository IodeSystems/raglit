package raglit

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iodesystems/agentkit/llm"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// TestLive_PDFOCRPipeline drives the real thing end-to-end: render text to an
// image, wrap it in a PDF, OCR each page through a vision model, index, and
// search. Guarded — runs only with RAGLIT_LIVE=1 (needs RAGLIT_LLM_KEY and a
// reachable vision endpoint; defaults to bonsai / gemma-4-12b).
//
//	RAGLIT_LIVE=1 RAGLIT_LLM_KEY=… go test -run TestLive ./...
func TestLive_PDFOCRPipeline(t *testing.T) {
	if os.Getenv("RAGLIT_LIVE") == "" {
		t.Skip("set RAGLIT_LIVE=1 (and RAGLIT_LLM_KEY) to run the live OCR pipeline")
	}
	dir := t.TempDir()
	const phrase = "Refresh token rotates on each use and is single-use."
	pdfPath := renderTextPDF(t, dir, phrase)

	base := envOr("RAGLIT_LLM_URL", "https://llm.iodesystems.com")
	modelID := envOr("RAGLIT_LLM_MODEL", "gemma-4-12b")
	client := llm.NewClient(base, os.Getenv("RAGLIT_LLM_KEY"), modelID)

	s, err := OpenHome(Home(filepath.Join(dir, "home")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	n, err := s.IngestPDF(ctx, NewOCR(client), pdfPath)
	if err != nil {
		t.Fatalf("IngestPDF: %v", err)
	}
	if n == 0 {
		t.Fatal("OCR produced no indexable text")
	}

	hits, err := s.Search("refresh token", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatalf("OCR'd text not searchable (model transcribed something unexpected)")
	}
	t.Logf("OCR→index→search OK: page %d text = %q", hits[0].Page, hits[0].Text)
}

// renderTextPDF draws phrase onto a white PNG (real TTF, large enough for OCR)
// and wraps it in a one-page image-PDF.
func renderTextPDF(t *testing.T, dir, phrase string) string {
	t.Helper()
	ttf, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: 40, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1100, 160))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.Black), Face: face, Dot: fixed.P(30, 95)}
	d.DrawString(phrase)

	pngPath := filepath.Join(dir, "page.png")
	f, err := os.Create(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := api.ImportImagesFile([]string{pngPath}, pdfPath, pdfcpu.DefaultImportConfig(), model.NewDefaultConfiguration()); err != nil {
		t.Fatal(err)
	}
	return pdfPath
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
