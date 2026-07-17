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

// OCR transcribes page images to text via a vision-capable chat model
// (e.g. gemma-4-12b on bonsai). It carries agentkit's multimodal llm.Message:
// a text instruction + the page as an inline image part.
type OCR struct {
	Client Chatter
	Prompt string // transcription instruction; "" → defaultOCRPrompt
}

// NewOCR wraps a Chatter (an *llm.Client) as an OCR transcriber.
func NewOCR(c Chatter) *OCR { return &OCR{Client: c} }

// Page transcribes one page image and returns the trimmed text.
func (o *OCR) Page(ctx context.Context, img PageImage) (string, error) {
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
