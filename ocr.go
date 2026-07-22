package raglit

import (
	"context"
	"fmt"
	"strings"

	"github.com/iodesystems/agentkit/llm"
)

// defaultOCRPrompt asks for a faithful transcription and nothing else — no
// summary, no markdown fences — so the indexed text is the page's words, not
// the model's commentary.
const defaultOCRPrompt = "Transcribe all text visible in this document page image exactly as it appears, " +
	"preserving reading order and line breaks. Output ONLY the transcription: no commentary, no headings you add yourself, no markdown code fences."

// Chatter is the sliver of *llm.Client the OCR path needs — one multimodal
// completion. An interface so the vision model can be stubbed in tests.
type Chatter interface {
	Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (string, []llm.ToolCall, error)
}

// OCR transcribes page images to text. It runs a CASCADE: an optional cheap
// first-pass engine (tesseract / paddleocr), gated by a gibberish detector, and
// falls back to a vision-capable chat model (e.g. gemma-4-12b on bonsai, via
// corrallm) only when the cheap pass is missing, errors, or looks like garbage.
// With no Cheap engine it is VLM-only — the original behavior.
type OCR struct {
	Client Chatter
	Prompt string          // transcription instruction; "" → defaultOCRPrompt
	Cheap  PageEngine      // optional cheap first pass; nil → VLM-only
	Gate   GibberishConfig // when the cheap pass escalates to the VLM (zero → defaults)
}

// NewOCR wraps a Chatter (an *llm.Client) as an OCR transcriber. The cheap tier
// is off by default; set OCR.Cheap (see BuildPageEngine) to enable the cascade.
func NewOCR(c Chatter) *OCR { return &OCR{Client: c} }

// Page transcribes one page image and returns the trimmed text.
func (o *OCR) Page(ctx context.Context, img PageImage) (string, error) {
	text, _, err := o.PageWithEngine(ctx, img)
	return text, err
}

// PageWithEngine transcribes one page and reports which engine produced it:
// the cheap engine's Name() when its result passed the gibberish gate, else
// "vision". The cascade never drops a page — a cheap-engine error or a
// gibberish verdict escalates to the VLM rather than returning the bad text.
func (o *OCR) PageWithEngine(ctx context.Context, img PageImage) (text, engine string, err error) {
	if o.Cheap != nil {
		if po, cerr := o.Cheap.OCRPage(ctx, img); cerr == nil {
			// A non-gibberish result (including a legitimately empty page) is
			// trusted — do not pay the VLM for clean or blank pages.
			if gib, _ := o.Gate.IsGibberish(po); !gib {
				return strings.TrimSpace(po.Text), o.Cheap.Name(), nil
			}
		}
		// cheap error or gibberish → fall through to the VLM.
	}
	t, verr := o.visionPage(ctx, img)
	if verr != nil {
		return "", "", verr
	}
	return t, "vision", nil
}

// visionPage is the VLM transcription: agentkit's multimodal llm.Message — a
// text instruction + the page as an inline image part.
func (o *OCR) visionPage(ctx context.Context, img PageImage) (string, error) {
	if o.Client == nil {
		return "", fmt.Errorf("raglit: ocr page %d needs the vision model but none is configured", img.Page)
	}
	prompt := o.Prompt
	if prompt == "" {
		prompt = defaultOCRPrompt
	}
	msg := llm.Message{Role: "user", Parts: []llm.ContentPart{
		llm.TextPart(prompt),
		llm.ImageData(img.Mime, img.Data),
	}}
	text, _, err := o.Client.Chat(ctx, []llm.Message{msg}, nil)
	if err != nil {
		return "", fmt.Errorf("raglit: ocr page %d: %w", img.Page, err)
	}
	return strings.TrimSpace(text), nil
}
