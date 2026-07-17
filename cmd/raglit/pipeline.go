package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/agentkit/llm"
	"github.com/iodesystems/raglit"
)

// addLLMFlags registers the vision-model connection flags and returns a builder.
// Defaults target bonsai (llm.iodesystems.com / gemma-4-12b); the key comes from
// --llm-key or $RAGLIT_LLM_KEY.
func addLLMFlags(fs *flag.FlagSet) func() *llm.Client {
	url := fs.String("llm-url", "https://llm.iodesystems.com", "vision model base URL")
	model := fs.String("llm-model", "ternary-bonsai-27b", "vision model id (must accept images)")
	key := fs.String("llm-key", "", "API key (or $RAGLIT_LLM_KEY)")
	return func() *llm.Client {
		k := *key
		if k == "" {
			k = os.Getenv("RAGLIT_LLM_KEY")
		}
		return llm.NewClient(*url, k, *model)
	}
}

// runPagify extracts page images from image/scanned PDFs.
func runPagify(args []string) error {
	fs := flag.NewFlagSet("pagify", flag.ExitOnError)
	out := fs.String("out", "pages", "output directory for page images")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("pagify: no PDF given")
	}
	for _, pdf := range fs.Args() {
		dir := filepath.Join(*out, strings.TrimSuffix(filepath.Base(pdf), filepath.Ext(pdf)))
		pages, err := raglit.Pagify(pdf, dir)
		if err != nil {
			return err
		}
		for _, p := range pages {
			fmt.Printf("p%d\t%s\t%s\n", p.Page, p.Mime, p.Path)
		}
		fmt.Fprintf(os.Stderr, "pagify: %s → %d page image(s) in %s\n", pdf, len(pages), dir)
	}
	return nil
}

// runOcr transcribes image files to text via the vision model (one per line
// separated by a form feed), for piping / inspection.
func runOcr(args []string) error {
	fs := flag.NewFlagSet("ocr", flag.ExitOnError)
	newLLM := addLLMFlags(fs)
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("ocr: no image files given")
	}
	ocr := raglit.NewOCR(newLLM())
	for _, img := range fs.Args() {
		data, err := os.ReadFile(img)
		if err != nil {
			return err
		}
		text, err := ocr.Page(context.Background(), raglit.PageImage{
			Mime: mimeForImage(img), Data: data,
		})
		if err != nil {
			return err
		}
		fmt.Printf("%s\n%s\n\f", img, text)
	}
	return nil
}

func isPDF(p string) bool { return strings.EqualFold(filepath.Ext(p), ".pdf") }

func isImage(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".tif", ".tiff", ".webp", ".gif":
		return true
	}
	return false
}

func mimeForImage(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
