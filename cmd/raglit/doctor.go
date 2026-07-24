package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// runDoctor reports OCR readiness: whether the configured cheap engine is
// runnable, whether the vision endpoint is reachable, and which cascade tiers
// are therefore available. It is the answer to "the user recalled tesseract
// being hard to install" — a one-shot check with the exact install hint.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	homeFlag := fs.String("home", "", "config home dir (default: nearest ./.raglit, else ~/local/raglit)")
	fs.Parse(args)
	home := raglit.DiscoverHome()
	if *homeFlag != "" {
		home = raglit.Home(*homeFlag)
	}

	cfg, exists, err := raglit.LoadConfig(home)
	if err != nil {
		return err
	}
	fmt.Printf("raglit doctor — OCR readiness\n  home:   %s\n", home)
	if !exists {
		fmt.Println("  config: NOT initialized — run `raglit init`")
	}

	// Vision (VLM fallback) tier.
	fmt.Println("\nvision (VLM) tier:")
	visionOK := cfg.VisionModel != ""
	if !visionOK {
		fmt.Println("  ✗ no vision_model configured — the cascade cannot fall back to a VLM")
	} else {
		fmt.Printf("  model:    %s\n  endpoint: %s\n", cfg.VisionModel, cfg.BaseURL)
		if ok, detail := pingEndpoint(cfg.BaseURL); ok {
			fmt.Printf("  ✓ endpoint reachable (%s)\n", detail)
		} else {
			fmt.Printf("  ✗ endpoint unreachable: %s\n", detail)
		}
	}

	// Cheap (first-pass) tier.
	fmt.Println("\ncheap (first-pass) tier:")
	cheapOK := false
	switch strings.ToLower(strings.TrimSpace(cfg.OCR.CheapEngine)) {
	case "", "none":
		fmt.Println("  · disabled (cheap_engine=none) — every page uses the VLM")
	case "tesseract":
		bin := cfg.OCR.TesseractBin
		if bin == "" {
			bin = "tesseract"
		}
		if v, e := tesseractVersion(bin); e == nil {
			fmt.Printf("  ✓ tesseract: %s (%s)\n", v, bin)
			cheapOK = true
		} else {
			fmt.Printf("  ✗ tesseract not runnable (%s): %v\n", bin, e)
			fmt.Println("     install:  sudo apt-get install tesseract-ocr tesseract-ocr-eng")
			fmt.Println("     no sudo:  deb-extract into a prefix — recipe in raglit/plan/ocr-mcp.md")
		}
	case "paddle", "paddleocr":
		if _, e := raglit.BuildPageEngine(cfg.OCR); e != nil {
			fmt.Printf("  ✗ %v\n", e)
		} else if ok, detail := pingEndpoint(cfg.OCR.PaddleURL); ok {
			fmt.Printf("  ✓ paddleocr reachable at %s (%s)\n", cfg.OCR.PaddleURL, detail)
			cheapOK = true
		} else {
			fmt.Printf("  ✗ paddleocr unreachable at %s: %s\n", cfg.OCR.PaddleURL, detail)
			fmt.Println("     run a PaddleOCR sidecar exposing POST /ocr (docker), then set ocr.paddle_url")
		}
	default:
		fmt.Printf("  ✗ unknown cheap_engine %q (want none|tesseract|paddleocr)\n", cfg.OCR.CheapEngine)
	}

	// Format extractors (the router's external tools).
	fmt.Println("\nformat extractors:")
	if raglit.HavePoppler() {
		fmt.Println("  ✓ poppler (pdftotext + pdftoppm) — PDF text layer + page rasterization")
	} else {
		fmt.Println("  ✗ poppler missing — born-digital PDFs can't extract their text layer")
		fmt.Println("     install:  sudo apt-get install poppler-utils   (no sudo? deb-extract, see plan)")
	}
	if raglit.HavePandoc() {
		fmt.Println("  ✓ pandoc — office/markup (docx, odt, epub, html, pptx) → text")
	} else {
		fmt.Println("  · pandoc missing — office/markup formats won't be extracted (optional)")
		fmt.Println("     install:  sudo apt-get install pandoc")
	}

	// Verdict — which tiers are live.
	fmt.Println("\nverdict:")
	switch {
	case cheapOK && visionOK:
		fmt.Println("  ✓ full cascade: cheap first-pass → gibberish gate → VLM fallback")
	case visionOK:
		fmt.Println("  ✓ VLM-only: every page transcribed by the vision model (no cheap tier)")
	case cheapOK:
		fmt.Println("  ⚠ cheap-only: clean pages OK, but a gibberish page has no VLM to escalate to")
	default:
		fmt.Println("  ✗ OCR unavailable: configure a vision_model and/or a cheap_engine")
	}
	return nil
}

// tesseractVersion runs `<bin> --version` and returns its first line (e.g.
// "tesseract 5.3.4"), or an error if the binary is missing / not runnable.
func tesseractVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]), nil
}

// pingEndpoint does a quick reachability GET. For an OpenAI base URL (…/v1) it
// hits …/v1/models; otherwise it hits the URL itself. A non-5xx status counts as
// reachable — a 401/404 still proves the service is up.
func pingEndpoint(base string) (ok bool, detail string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return false, "no URL configured"
	}
	url := base
	if strings.HasSuffix(base, "/v1") {
		url = base + "/models"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500, fmt.Sprintf("HTTP %d", resp.StatusCode)
}
