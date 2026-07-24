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

// llmFlags holds the shared model-connection flags. One endpoint + key, two
// models: a vision model for OCR and an embedding model for vectors. Flags
// default EMPTY; resolve() fills them from `raglit init` config (then env, then
// an OpenAI-standard fallback for the URL), so explicit flags override config.
type llmFlags struct {
	url, key, visionModel, embedModel *string
	contextTokens                     *int
}

func addLLMFlags(fs *flag.FlagSet) *llmFlags {
	return &llmFlags{
		url:           fs.String("llm-url", "", "model base URL (default: config, else OpenAI)"),
		key:           fs.String("llm-key", "", "API key (default: config or $RAGLIT_LLM_KEY)"),
		visionModel:   fs.String("llm-model", "", "vision model id (default: config)"),
		embedModel:    fs.String("embed-model", "", "embedding model id (default: config)"),
		contextTokens: fs.Int("context-tokens", 0, "known model context window (tokens); 0 = config/probe"),
	}
}

// resolve fills any unset flag from the home's config, then env, then a sane
// fallback. Precedence: explicit flag > config > env > hardcoded.
func (f *llmFlags) resolve(home raglit.Home) {
	cfg, _, _ := raglit.LoadConfig(home)
	*f.url = firstNonEmpty(*f.url, cfg.BaseURL, "https://api.openai.com/v1")
	*f.visionModel = firstNonEmpty(*f.visionModel, cfg.VisionModel)
	*f.embedModel = firstNonEmpty(*f.embedModel, cfg.EmbedModel)
	*f.key = firstNonEmpty(*f.key, os.Getenv("RAGLIT_LLM_KEY"), cfg.APIKey)
}

func (f *llmFlags) requireVision() error {
	if *f.visionModel == "" {
		return fmt.Errorf("no vision model configured — run 'raglit init' or pass --llm-model")
	}
	return nil
}

func (f *llmFlags) requireEmbed() error {
	if *f.embedModel == "" {
		return fmt.Errorf("no embedding model configured — run 'raglit init' or pass --embed-model")
	}
	return nil
}

func (f *llmFlags) visionClient() *llm.Client {
	return llm.NewClient(*f.url, *f.key, *f.visionModel)
}

// attachCheapOCR enables the cascade's cheap first-pass engine on an OCR from the
// home's config (config.OCR). A misconfigured engine degrades to VLM-only with a
// warning rather than failing — a bad OCR knob must not break ingestion.
func attachCheapOCR(ocr *raglit.OCR, home raglit.Home) {
	if ocr == nil {
		return
	}
	cfg, _, err := raglit.LoadConfig(home)
	if err != nil {
		return
	}
	eng, err := raglit.BuildPageEngine(cfg.OCR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit: %v — OCR falling back to vision-only\n", err)
		return
	}
	if eng != nil {
		ocr.Cheap = eng
		ocr.Gate = cfg.OCR.Gibberish
	}
}

func (f *llmFlags) embedder() *raglit.Embedder {
	return raglit.NewEmbedder(llm.NewClient(*f.url, *f.key, *f.embedModel), *f.embedModel)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
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
	lf := addLLMFlags(fs)
	homeFlag := fs.String("home", "", "config home dir (for defaults)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("ocr: no image files given")
	}
	home := raglit.DiscoverHome()
	if *homeFlag != "" {
		home = raglit.Home(*homeFlag)
	}
	lf.resolve(home)
	if err := lf.requireVision(); err != nil {
		return err
	}
	ocr := raglit.NewOCR(lf.visionClient())
	attachCheapOCR(ocr, home)
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
