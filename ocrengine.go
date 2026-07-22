package raglit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// OCRLine is one recognized text box from a cheap page-OCR engine, with the
// recognizer's confidence (0..1). The gibberish gate reads the per-line scores
// in aggregate (MeanConfidence); downstream only the joined Text matters.
type OCRLine struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// PageOCR is a cheap engine's result for one page image. Text is the recognized
// lines joined in reading order; MeanConfidence and BoxCount are the cheap
// signals the gibberish gate fuses to decide whether the page needs a
// vision-model re-OCR. BoxCount == 0 means no text at all (blank page or a pure
// figure) — treated as an empty page, not gibberish.
type PageOCR struct {
	Text           string
	Lines          []OCRLine
	MeanConfidence float64
	BoxCount       int
}

// PageEngine is a CHEAP first-pass OCR over one rendered page image — the tier
// the cascade tries before paying for the vision model (see OCR.Page). It is the
// swappable "other OCR system" slot: a local CLI (tesseract) or a bespoke sidecar
// (paddleocr) are interchangeable here.
type PageEngine interface {
	OCRPage(ctx context.Context, img PageImage) (PageOCR, error)
	Name() string // short id for the per-page engine tag: "tesseract" | "paddleocr"
}

// BuildPageEngine constructs the configured cheap engine, or nil for "none" (the
// cascade is then VLM-only). An unknown name or a paddleocr backend with no URL
// is a configuration error the caller should surface.
func BuildPageEngine(cfg OCRConfig) (PageEngine, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.CheapEngine)) {
	case "", "none":
		return nil, nil
	case "tesseract":
		return NewTesseractEngine(cfg.TesseractBin, cfg.TesseractLang), nil
	case "paddle", "paddleocr":
		if strings.TrimSpace(cfg.PaddleURL) == "" {
			return nil, fmt.Errorf("raglit: ocr.cheap_engine=paddleocr but ocr.paddle_url is empty")
		}
		return NewPaddleEngine(cfg.PaddleURL), nil
	default:
		return nil, fmt.Errorf("raglit: unknown ocr.cheap_engine %q (want none|tesseract|paddleocr)", cfg.CheapEngine)
	}
}

// --- tesseract (in-process, exec — the footprint-light default) --------------

// TesseractEngine runs the tesseract CLI over each page: no cgo, no daemon, one
// optional system dependency. Fast and ~free relative to a vision LLM, but blind
// to handwriting and stylized figures — which is exactly what the gibberish gate
// exists to catch and escalate.
type TesseractEngine struct {
	Bin  string // tesseract binary; "" → "tesseract"
	Lang string // -l language; "" → "eng"
}

// NewTesseractEngine builds a TesseractEngine with defaults filled.
func NewTesseractEngine(bin, lang string) *TesseractEngine {
	if bin == "" {
		bin = "tesseract"
	}
	if lang == "" {
		lang = "eng"
	}
	return &TesseractEngine{Bin: bin, Lang: lang}
}

func (e *TesseractEngine) Name() string { return "tesseract" }

// OCRPage pipes the page image to `tesseract stdin stdout -l <lang> tsv` and
// parses the TSV (per-word confidence + line grouping). A run error propagates
// so the cascade falls back to the vision model rather than dropping the page.
func (e *TesseractEngine) OCRPage(ctx context.Context, img PageImage) (PageOCR, error) {
	cmd := exec.CommandContext(ctx, e.Bin, "stdin", "stdout", "-l", e.Lang, "tsv")
	cmd.Stdin = bytes.NewReader(img.Data)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return PageOCR{}, fmt.Errorf("tesseract: run: %w (%s)", err, strings.TrimSpace(errb.String()))
	}
	return parseTesseractTSV(out.String()), nil
}

// parseTesseractTSV builds a PageOCR from tesseract's TSV output: word rows
// (level 5) carry confidence (0..100) and text; words are joined with a space
// within a line and a newline across (block/par/line) boundaries. Rows with
// conf < 0 (no recognition) are skipped. BoxCount is the recognized-word count,
// so an empty page yields BoxCount 0 (the gate's empty-page short-circuit).
func parseTesseractTSV(tsv string) PageOCR {
	var (
		text    strings.Builder
		confSum float64
		words   int
		prevKey string
	)
	for i, ln := range strings.Split(tsv, "\n") {
		if i == 0 || ln == "" { // header / trailing blank
			continue
		}
		cols := strings.Split(ln, "\t")
		if len(cols) < 12 || cols[0] != "5" { // word level only
			continue
		}
		word := strings.TrimSpace(cols[11])
		if word == "" {
			continue
		}
		conf, err := strconv.ParseFloat(cols[10], 64)
		if err != nil || conf < 0 { // -1 = no recognition
			continue
		}
		key := cols[2] + "/" + cols[3] + "/" + cols[4] // block/par/line
		if words > 0 {
			if key == prevKey {
				text.WriteByte(' ')
			} else {
				text.WriteByte('\n')
			}
		}
		prevKey = key
		text.WriteString(word)
		confSum += conf
		words++
	}
	po := PageOCR{Text: strings.TrimSpace(text.String()), BoxCount: words}
	if words > 0 {
		po.MeanConfidence = (confSum / float64(words)) / 100.0
	}
	return po
}

var _ PageEngine = (*TesseractEngine)(nil)

// --- paddleocr (HTTP sidecar — ported from ragtag) ---------------------------

// PaddleEngine is a PageEngine backed by an out-of-process PaddleOCR sidecar
// (the one ragtag used; optionally installed via docker). It POSTs the page
// image to <BaseURL>/ocr and decodes the bespoke {text, lines, mean_confidence,
// box_count} contract. Higher print accuracy than tesseract, at the cost of a
// running sidecar.
type PaddleEngine struct {
	BaseURL string
	HTTP    *http.Client
}

// NewPaddleEngine builds a PaddleEngine for the sidecar base URL. The timeout is
// generous: CPU PP-OCR on a dense page can take a couple seconds.
func NewPaddleEngine(baseURL string) *PaddleEngine {
	return &PaddleEngine{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *PaddleEngine) Name() string { return "paddleocr" }

type paddleResponse struct {
	Text           string    `json:"text"`
	Lines          []OCRLine `json:"lines"`
	MeanConfidence float64   `json:"mean_confidence"`
	BoxCount       int       `json:"box_count"`
}

// OCRPage sends one page image to the sidecar and returns its PageOCR. The
// Content-Type is taken from the page's mime (the sidecar decodes png as readily
// as jpeg), defaulting to image/jpeg. Transport / decode / non-200 errors
// propagate so the cascade can fall back to the vision model.
func (e *PaddleEngine) OCRPage(ctx context.Context, img PageImage) (PageOCR, error) {
	if e == nil || e.BaseURL == "" {
		return PageOCR{}, fmt.Errorf("paddle: engine not configured")
	}
	client := e.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/ocr", bytes.NewReader(img.Data))
	if err != nil {
		return PageOCR{}, fmt.Errorf("paddle: build request: %w", err)
	}
	ct := img.Mime
	if ct == "" {
		ct = "image/jpeg"
	}
	req.Header.Set("Content-Type", ct)
	resp, err := client.Do(req)
	if err != nil {
		return PageOCR{}, fmt.Errorf("paddle: post: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return PageOCR{}, fmt.Errorf("paddle: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return PageOCR{}, fmt.Errorf("paddle: sidecar status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var pr paddleResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return PageOCR{}, fmt.Errorf("paddle: decode response: %w", err)
	}
	return PageOCR{Text: pr.Text, Lines: pr.Lines, MeanConfidence: pr.MeanConfidence, BoxCount: pr.BoxCount}, nil
}

var _ PageEngine = (*PaddleEngine)(nil)
