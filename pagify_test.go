package raglit

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// writePNG writes a w×h white PNG (enough to exercise the image pipeline).
func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.White)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestPagify_ExtractsEmbeddedPageImages(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "src.png")
	writePNG(t, pngPath, 16, 16)

	// Build a one-page image-PDF from the PNG (an image/scanned PDF, which is
	// exactly what pagify targets).
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := api.ImportImagesFile([]string{pngPath}, pdfPath, pdfcpu.DefaultImportConfig(), model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("build fixture pdf: %v", err)
	}

	out := filepath.Join(dir, "pages")
	pages, err := Pagify(pdfPath, out)
	if err != nil {
		t.Fatalf("pagify: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("want 1 page image, got %d", len(pages))
	}
	p := pages[0]
	if p.Page != 1 || len(p.Data) == 0 || p.Mime == "" {
		t.Fatalf("bad page image: %+v", p)
	}
	if _, err := os.Stat(p.Path); err != nil {
		t.Fatalf("page image not written to disk: %v", err)
	}
}

func TestPagify_MissingFile(t *testing.T) {
	if _, err := Pagify(filepath.Join(t.TempDir(), "nope.pdf"), ""); err == nil {
		t.Fatal("expected an error for a missing pdf")
	}
}
