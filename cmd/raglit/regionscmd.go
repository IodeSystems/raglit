package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iodesystems/raglit"
)

// runRegions reads one page as a tree of regions.
//
// Separate from `ocr` because it answers a different question. `ocr` asks "what
// does this page say"; this asks "what is here, and where do I need to look
// closer" — which is the only workable question for a sheet whose text is
// physically too small to survive one look.
func runRegions(args []string) error {
	fs := flag.NewFlagSet("regions", flag.ExitOnError)
	lf := addLLMFlags(fs)
	_, homeOf := addStoreFlags(fs)
	page := fs.Int("page", 1, "page number")
	dpi := fs.Int("dpi", 200, "render resolution")
	depth := fs.Int("depth", 0, "descend this many levels into the sheet (0 = read the whole sheet and stop)")
	calls := fs.Int("max-calls", 40, "model calls allowed for the whole page")
	asJSON := fs.Bool("json", false, "emit the region tree as JSON")
	write := fs.Bool("write", false, "record the read in <doc>.raglit-regions.json beside the document")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: raglit regions [flags] FILE")
	}
	path := fs.Arg(0)
	lf.resolve(homeOf())
	if err := lf.requireVision(); err != nil {
		return err
	}

	wIn, hIn, err := pageSizeInches(path, *page)
	if err != nil {
		return err
	}
	img, err := renderPage(path, *page, *dpi)
	if err != nil {
		return err
	}
	b := img.Bounds()
	fmt.Fprintf(os.Stderr, "page %d: %.1f x %.1f in (%.0f sq in), rendered %dx%d at %d dpi\n",
		*page, wIn, hIn, wIn*hIn, b.Dx(), b.Dy(), *dpi)

	ocr := raglit.NewOCR(lf.visionClient())
	rr := &raglit.RegionReader{
		Ask: ocr.AskWithOCR(), PageWIn: wIn, PageHIn: hIn, DPI: *dpi,
		MaxDepth: *depth, MaxCalls: *calls,
	}
	root, rerr := rr.Read(context.Background(), img, *page)
	// A partial tree is still worth printing — and worth RECORDING: the whole
	// design is that a failed descent costs detail rather than coverage, and a
	// region that was read before the failure has text somebody may have to
	// attest.
	if *write {
		text, spans := raglit.RegionTranscript(root)
		out, werr := raglit.WriteRegionDoc(path, raglit.RegionPage{
			Page: *page, WidthIn: wIn, HeightIn: hIn, DPI: *dpi,
			PxW: b.Dx(), PxH: b.Dy(), Root: root, Text: text, Spans: spans,
		})
		if werr != nil {
			return werr
		}
		fmt.Fprintf(os.Stderr, "recorded %d region(s) → %s\n", len(root.Flatten()), out)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(root); err != nil {
			return err
		}
	} else {
		fmt.Print(root)
		fmt.Fprintf(os.Stderr, "\n%d region(s), %d leaf/leaves\n", len(root.Flatten()), len(root.Leaves()))
	}
	return rerr
}

// pageSizeInches reads a page's PHYSICAL size, which is the input that decides
// everything here: the encoder's budget is per image, so square inches are what
// resolution is spent on.
func pageSizeInches(path string, page int) (float64, float64, error) {
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		// An image file has no physical size; assume letter so the arithmetic
		// stays meaningful rather than dividing by a guess of zero.
		return 8.5, 11, nil
	}
	out, err := exec.Command("pdfinfo", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), path).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("pdfinfo: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "size:") {
			continue
		}
		f := strings.Fields(line)
		for i, tok := range f {
			if tok == "x" && i > 0 {
				w, e1 := strconv.ParseFloat(f[i-1], 64)
				h, e2 := strconv.ParseFloat(f[i+1], 64)
				if e1 == nil && e2 == nil {
					return w / 72, h / 72, nil // points to inches
				}
			}
		}
	}
	return 0, 0, fmt.Errorf("could not read page size from pdfinfo")
}

func renderPage(path string, page, dpi int) (image.Image, error) {
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		im, _, derr := image.Decode(f)
		return im, derr
	}
	dir, err := os.MkdirTemp("", "raglit-regions-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "p")
	ps := strconv.Itoa(page)
	cmd := exec.Command("pdftoppm", "-png", "-r", strconv.Itoa(dpi),
		"-f", ps, "-l", ps, "-singlefile", path, prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	f, err := os.Open(prefix + ".png")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, _, derr := image.Decode(f)
	return im, derr
}
