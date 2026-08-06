package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iodesystems/agentkit/llm"
	"github.com/iodesystems/raglit"
)

// llmFlags holds the shared model-connection flags. One endpoint + key, two
// models: a vision model for OCR and an embedding model for vectors. Flags
// default EMPTY; resolve() fills them from `raglit init` config (then env, then
// an OpenAI-standard fallback for the URL), so explicit flags override config.
type llmFlags struct {
	url, key, visionModel, embedModel *string
}

func addLLMFlags(fs *flag.FlagSet) *llmFlags {
	return &llmFlags{
		url:         fs.String("llm-url", "", "model base URL (default: config, else OpenAI)"),
		key:         fs.String("llm-key", "", "API key (default: config or $RAGLIT_LLM_KEY)"),
		visionModel: fs.String("llm-model", "", "vision model id (default: config)"),
		embedModel:  fs.String("embed-model", "", "embedding model id (default: config)"),
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
	c := llm.NewClient(*f.url, *f.key, *f.visionModel)
	// Ingest is a BATCH, not a chat turn, and agentkit's default 5xx cap of five
	// attempts is tuned for the latter. A 33-page document is ~33 requests, so
	// one upstream blip outlasting ~15s of backoff kills the whole document —
	// measured on a real corpus, nothing over 20 pages ever completed.
	//
	// The wall clock is still bounded by RetryBudget, which is the guard that
	// should bind here. Raising attempts trades a longer wait on a sick upstream
	// for finishing the document, which for a transcription backlog is the right
	// trade: the alternative is not "fail fast", it is "re-read 30 pages".
	c.Retry5xxAttempts = visionRetry5xxAttempts
	return c
}

// visionRetry5xxAttempts rides out an upstream episode of roughly two minutes.
// With exponential backoff capped at 30s the schedule is about
// 1+2+4+8+16+30+30+30s; RetryBudget still stops it after that.
const visionRetry5xxAttempts = 9

// attachCheapOCR enables the cascade's cheap first-pass engine on an OCR from the
// home's config (config.OCR). A misconfigured engine degrades to VLM-only with a
// warning rather than failing — a bad OCR knob must not break ingestion.
// strategy names an ocr.strategies entry to use instead of the project default.
// Empty → the project default. An unknown name is REPORTED and then ignored:
// silently falling back would make a typo look like a strategy that had no
// effect, which is indistinguishable from one that did nothing on purpose.
func attachCheapOCR(ocr *raglit.OCR, home raglit.Home, strategy string) {
	if ocr == nil {
		return
	}
	cfg, _, err := raglit.LoadConfig(home)
	if err != nil {
		return
	}
	ocr.DescribeFigures = cfg.OCR.DescribeFigures
	// The project's default strategy supplies the resolution policy. Set before
	// the engine build and outside the eng != nil guard, because BaseDPI governs
	// even when nothing can measure glyph height — that is the branch
	// renderDPIFor takes with a nil engine.
	//
	// PROJECT default, not per-index: this is built once per command, and the
	// index a document belongs to is not known here. Threading it is what the
	// per-index `ocr_strategy` still needs to take effect on the ingest path.
	st := cfg.StrategyFor("")
	if strategy != "" {
		named, ok := cfg.StrategyNamed(strategy)
		if !ok {
			fmt.Fprintf(os.Stderr, "raglit: no ocr strategy named %q — using the project default\n", strategy)
		} else {
			st = named
		}
	}
	ocr.Render = st.Render
	eng, err := raglit.BuildPageEngine(cfg.OCR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit: %v — OCR falling back to vision-only\n", err)
		return
	}
	if eng != nil {
		ocr.Cheap = eng
		ocr.Gate = cfg.OCR.Gibberish
		ocr.Assist = strings.EqualFold(strings.TrimSpace(cfg.OCR.Mode), "assist")
	}
}

// identifier builds the document-identity model client from config, or returns
// nil when identity is off or no model is configured.
//
// It defaults to the VISION model because that is the one every raglit home
// already has, and it is a chat model — the identity call is text in, JSON out,
// so a vision model serves it. config.identity_model overrides.
//
// Same batch retry policy as the rest of ingest: this call happens at the end of
// a document that may have cost thirty OCR requests, and losing the caption to a
// transient 502 wastes the one moment the whole transcript is in hand.
func (f *llmFlags) identifier(home raglit.Home) *raglit.Identifier {
	cfg, _, _ := raglit.LoadConfig(home)
	if cfg.NoIdentity {
		return nil
	}
	model := firstNonEmpty(cfg.IdentityModel, *f.visionModel)
	if model == "" {
		return nil
	}
	c := llm.NewClient(*f.url, *f.key, model)
	c.Retry5xxAttempts = visionRetry5xxAttempts
	return raglit.NewIdentifier(c, model)
}

// embedClientForProbe is the embed client with the same batch retry policy as
// ingest. The embedder was the ONE client built without it, which is why its
// failures logged a different retry cap than the OCR client and sent an
// investigation down the wrong path.
func (f *llmFlags) embedClientForProbe() *llm.Client {
	c := llm.NewClient(*f.url, *f.key, *f.embedModel)
	c.Retry5xxAttempts = visionRetry5xxAttempts
	return c
}

func (f *llmFlags) embedder() *raglit.Embedder {
	return raglit.NewEmbedder(f.embedClientForProbe(), *f.embedModel)
}

// buildImageEmbedder returns a figure IMAGE embedder from config (nomic-vision),
// or nil when none is configured (figures then embed their description). The API
// key defaults to the main config key.
func buildImageEmbedder(home raglit.Home) raglit.ImageEmbedder {
	cfg, _, _ := raglit.LoadConfig(home)
	if cfg.ImageEmbed.Model == "" {
		return nil
	}
	key := cfg.ImageEmbed.APIKey
	if key == "" {
		key = cfg.APIKey
	}
	return raglit.NewNomicVisionEmbedder(cfg.ImageEmbed.URL, key, cfg.ImageEmbed.Model)
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
	verbose := fs.Bool("verbose", false,
		"report what the read DID on stderr: cheap tier, assist, tools offered, downscales, timing")
	strategy := fs.String("strategy", "",
		"force a named ocr.strategies entry; empty → the project default (and, once detection lands, whatever the page measures as)")
	traceDir := fs.String("trace", "",
		"write a machine-readable run record to DIR/log.jsonl: one JSON object per interaction, with the image sha, pixel size, estimated image tokens and the transforms applied")
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
	ocr.Model = *lf.visionModel
	attachCheapOCR(ocr, home, *strategy)
	// --trace is INDEPENDENT of --verbose: one is prose for a person watching,
	// the other is a record to diff and join on later. Asking for the record
	// should not also flood the terminal.
	if *traceDir != "" {
		f, err := openTraceLog(*traceDir)
		if err != nil {
			return err
		}
		defer f.Close()
		ocr.TraceJSONL = f
	}
	if *verbose {
		// stderr, so a verbose run still pipes its transcription cleanly.
		ocr.Trace = os.Stderr
		fmt.Fprintf(os.Stderr, "  ocr | home %s, url %s\n", home, *lf.url)
	}
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

// runTranscribe writes a document out as page-delineated markdown.
//
// The pipeline already produces this per page and then discards it once
// fragments are built, which is why every consumer that needed "what is on page
// 7" reimplemented rasterize-and-OCR outside raglit. `transcribe` is that
// output, on purpose and on demand.
func runTranscribe(args []string) error {
	fs := flag.NewFlagSet("transcribe", flag.ExitOnError)
	lf := addLLMFlags(fs)
	homeFlag := fs.String("home", "", "config home dir (for defaults)")
	write := fs.Bool("write", false, "write <doc>.raglit-transcription.md beside each document instead of stdout")
	force := fs.Bool("force", false, "with --write, redo one that already exists")
	correct := fs.Bool("correct", false, "record corrected text for one page, read from stdin")
	page := fs.Int("page", 0, "with --correct: which page the corrected text is for")
	note := fs.String("note", "", "with --correct: how the correction was established (crop, dpi, magnification)")
	by := fs.String("by", "", "with --correct: who checked it (default $RAGLIT_BY, else the OS user)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("transcribe: no files given")
	}
	if *correct {
		if fs.NArg() != 1 {
			return fmt.Errorf("transcribe --correct: name one document")
		}
		return runCorrectPage(fs.Arg(0), *page, *note, *by)
	}
	home := raglit.DiscoverHome()
	if *homeFlag != "" {
		home = raglit.Home(*homeFlag)
	}
	lf.resolve(home)

	// OCR is only needed for pages with no text layer. Build it when we can, and
	// let ExtractPaged report the specific page if a scan turns up without one.
	var ocr *raglit.OCR
	if lf.requireVision() == nil {
		ocr = raglit.NewOCR(lf.visionClient())
		ocr.Model = *lf.visionModel
		attachCheapOCR(ocr, home, "")
	}

	// Corrections are re-issued into every render. A page a person checked stays
	// checked across re-reads; that is the whole point of storing them outside
	// the file this writes.
	var js *raglit.JudgementStore
	if j, err := openJudgements(); err == nil {
		js = j
		defer js.Close()
	}

	for _, path := range fs.Args() {
		out := raglit.TranscriptionPath(path)
		if *write && !*force {
			if _, err := os.Stat(out); err == nil {
				fmt.Fprintf(os.Stderr, "-- skip (has transcription): %s\n", filepath.Base(path))
				continue
			}
		}
		pages, err := raglit.ExtractPaged(context.Background(), path, ocr)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		tp := make([]raglit.TranscribedPage, 0, len(pages))
		for _, p := range pages {
			tp = append(tp, raglit.TranscribedPage{Page: p.Page, Text: p.Text})
		}
		if !*write {
			fmt.Print(raglit.RenderTranscriptionCorrected(path, tp, correctionsFor(js, path)))
			continue
		}
		if _, err := raglit.WriteTranscriptionCorrected(path, tp, correctionsFor(js, path)); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "   -> %s  (%d page(s))\n", filepath.Base(out), len(tp))
	}
	return nil
}

// runCorrectPage records what a person read off a page, so every later render
// re-issues it.
//
// The correction does NOT go into the .raglit-transcription.md file. That file
// is regenerated on every read — it is an export for tools that do not link
// raglit — and corrections kept in it were destroyed twice by ordinary re-reads
// of one survey. They go into the judgement store, which is projected from the
// audit trail and survives a reindex, and rendering applies them.
func runCorrectPage(doc string, page int, note, by string) error {
	if page < 1 {
		return fmt.Errorf("transcribe --correct: --page must be 1 or greater")
	}
	text, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("transcribe --correct: reading corrected text from stdin: %w", err)
	}
	if strings.TrimSpace(string(text)) == "" {
		return fmt.Errorf("transcribe --correct: no corrected text on stdin")
	}
	abs, err := filepath.Abs(doc)
	if err != nil {
		return err
	}
	js, err := openJudgements()
	if err != nil {
		return err
	}
	defer js.Close()

	if prev, _ := js.PageCorrections(abs); len(prev) > 0 {
		if _, ok := prev[page]; ok {
			// Said out loud: replacing somebody's checked reading silently is how
			// the work this exists to preserve gets lost a third way.
			fmt.Fprintf(os.Stderr, "note: page %d was already corrected — the previous reading is kept, marked superseded\n", page)
		}
	}

	// Prefer the daemon. It owns the index, and a correction changes the ACTIVE
	// reading — whose row history lives there. Falling back to a local append
	// keeps the correction durable when no daemon is reachable; the rows are
	// then written by whichever ingest next sees it.
	if n, derr := daemonCorrectPage(abs, page, note, who(by), text); derr == nil {
		fmt.Printf("page %d of %s corrected (%d chars)\n", page, filepath.Base(doc), len(text))
		fmt.Printf("  %d reading(s) on record for that page; the newest is active\n", n)
		return nil
	} else {
		fmt.Fprintf(os.Stderr, "note: no daemon (%v) — recording locally; reading rows follow on next ingest\n", derr)
	}

	if err := js.PutPageCorrection(raglit.PageCorrection{
		Doc: abs, Page: page, Text: string(text), Note: note,
		By: who(by), At: time.Now().UTC().Format("2006-01-02"),
	}); err != nil {
		return err
	}
	fmt.Printf("page %d of %s corrected (%d chars)\n", page, filepath.Base(doc), len(text))
	fmt.Println("  recorded in the audit trail; every later transcription re-issues it")
	return nil
}

// correctionsFor loads a document's page corrections, or nothing when no
// judgement store is reachable. A transcription without them is still a
// transcription; refusing to render one because a store could not be opened
// would make raglit unusable outside a project.
func correctionsFor(js *raglit.JudgementStore, path string) map[int]raglit.PageCorrection {
	if js == nil {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	c, err := js.PageCorrections(abs)
	if err != nil {
		return nil
	}
	return c
}

// openTraceLog creates dir and opens dir/log.jsonl for APPEND.
//
// Append, not truncate, so several runs into one directory accumulate instead of
// the last one erasing the comparison — which is the whole reason to keep a
// record. Each line already carries its own model and timestamp, so a mixed file
// separates by filtering rather than by having been kept apart.
func openTraceLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("trace dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "log.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("trace log: %w", err)
	}
	return f, nil
}
