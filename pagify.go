package raglit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// ErrNoPageImages is returned when a PDF has no embedded page images — i.e. a
// born-digital, vector-text PDF. raglit's pagify is pure-Go (pdfcpu) and pulls
// images that are ALREADY in the PDF (scanned / image PDFs); it does not
// rasterize vector pages. Rasterization would need a native renderer, out of
// scope by design.
var ErrNoPageImages = errors.New("raglit: no embedded page images (born-digital PDF? pagify handles image/scanned PDFs only)")

// PageImage is one page's image, ready for OCR.
type PageImage struct {
	Page int    // 1-based page number
	Path string // where it was written (empty when outDir == "")
	Mime string // "image/png", "image/jpeg", …
	Data []byte // raw image bytes
}

// Pagify extracts embedded page images from an image/scanned PDF, in page
// order. If outDir is non-empty each image is also written there as
// p<NN>.<ext> (with -<objnr> appended when a page carries multiple images).
// Returns ErrNoPageImages for a PDF with nothing to extract.
func Pagify(pdfPath, outDir string) ([]PageImage, error) {
	f, err := os.Open(pdfPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, err
		}
	}

	var pages []PageImage
	digest := func(img model.Image, singlePerPage bool, maxDigits int) error {
		data, err := io.ReadAll(img)
		if err != nil {
			return err
		}
		ext := img.FileType
		if ext == "" {
			ext = "png"
		}
		pi := PageImage{Page: img.PageNr, Mime: mimeForExt(ext), Data: data}
		if outDir != "" {
			name := fmt.Sprintf("p%0*d", maxDigits, img.PageNr)
			if !singlePerPage {
				name += fmt.Sprintf("-%d", img.ObjNr)
			}
			pi.Path = filepath.Join(outDir, name+"."+ext)
			if err := os.WriteFile(pi.Path, data, 0o644); err != nil {
				return err
			}
		}
		pages = append(pages, pi)
		return nil
	}

	if err := api.ExtractImages(f, nil, digest, model.NewDefaultConfiguration()); err != nil {
		return nil, fmt.Errorf("raglit: pagify: %w", err)
	}
	if len(pages) == 0 {
		return nil, ErrNoPageImages
	}
	return pages, nil
}

func mimeForExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "tif", "tiff":
		return "image/tiff"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
