package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iodesystems/agentkit/agent"
	"github.com/iodesystems/agentkit/llm"
)

// LLM-driven fragmentation.
//
// Instead of splitting text on blank lines (crude) or storing a whole OCR'd
// page as one blob, the model READS a unit — a page image, or a window of
// text/code — and returns coherent retrieval fragments PLUS whether the first
// fragment continues an "open" fragment carried over from the previous unit.
// One code path, two modalities (image vs text): only the content part differs.
//
// Two invariants make this safe on a small model:
//   - Output is schema-validated (agent.SchemaValidator over an emit_fragments
//     ToolDef) with a fix-loop; if it still won't produce valid JSON, we fall
//     back to "the whole unit is one fragment" — degrading to the old behavior,
//     never erroring.
//   - The ASSEMBLER (below) defers the open fragment: it is not finalized (and
//     so not embedded) until the next unit says whether it continues.

// Segment is one fragment the model carved out of a unit.
type Segment struct {
	Text string `json:"text"`
}

// SegResult is the model's structured segmentation of one unit.
type SegResult struct {
	ContinuesPrevious bool      `json:"continues_previous"`
	Fragments         []Segment `json:"fragments"`
}

// fragmentsToolDef is the schema SchemaValidator enforces on the model output.
func fragmentsToolDef() llm.ToolDef {
	var td llm.ToolDef
	td.Type = "function"
	td.Function.Name = "emit_fragments"
	td.Function.Description = "Emit the segmented fragments of a document unit."
	td.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"continues_previous": map[string]any{"type": "boolean"},
			"fragments": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			},
		},
		"required": []string{"continues_previous", "fragments"},
	}
	return td
}

// Segmenter runs schema-validated LLM segmentation with a fix-loop.
type Segmenter struct {
	Client     Chatter
	MaxRetries int             // JSON fix-loop attempts after the first try (default 2)
	validator  *agent.SchemaValidator
}

// NewSegmenter builds a Segmenter over a chat client (an *llm.Client).
func NewSegmenter(c Chatter) *Segmenter {
	return &Segmenter{
		Client:     c,
		MaxRetries: 2,
		validator:  agent.NewSchemaValidator([]llm.ToolDef{fragmentsToolDef()}),
	}
}

// SegmentImage segments a page image (PDF/scanned). openText is the carried-over
// open fragment (empty on the first unit).
func (sg *Segmenter) SegmentImage(ctx context.Context, mime string, data []byte, openText string) (SegResult, error) {
	parts := []llm.ContentPart{
		llm.TextPart(segPrompt(openText)),
		llm.ImageData(mime, data),
	}
	return sg.run(ctx, parts, "") // image fallback = the model's raw last output
}

// SegmentText segments a window of text/code.
func (sg *Segmenter) SegmentText(ctx context.Context, text, openText string) (SegResult, error) {
	parts := []llm.ContentPart{
		llm.TextPart(segPrompt(openText) + "\n\nCONTENT:\n" + text),
	}
	return sg.run(ctx, parts, text) // text fallback = the window itself
}

// run performs the validate/retry/fallback loop. fallback is the fragment text
// used when the model never yields valid JSON ("" → use its last raw output).
func (sg *Segmenter) run(ctx context.Context, parts []llm.ContentPart, fallback string) (SegResult, error) {
	msgs := []llm.Message{{Role: "user", Parts: parts}}
	var last string
	var lastErr error
	for attempt := 0; attempt <= sg.MaxRetries; attempt++ {
		out, _, err := sg.Client.Chat(ctx, msgs, nil)
		if err != nil {
			return SegResult{}, err // infrastructure failure → propagate (job fails)
		}
		last = out
		js := extractJSON(out)
		if lastErr = sg.validator.ValidateArgs("emit_fragments", js); lastErr == nil {
			var r SegResult
			if err := json.Unmarshal([]byte(js), &r); err != nil {
				lastErr = fmt.Errorf("unparseable: %v", err)
			} else if len(r.Fragments) == 0 {
				lastErr = fmt.Errorf("no fragments")
			} else {
				return r, nil
			}
		}
		// Re-prompt with the specific failure.
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: out},
			llm.Message{Role: "user", Content: fmt.Sprintf(
				"That was not valid: %v. Output ONLY the JSON object %s.",
				lastErr, `{"continues_previous":<bool>,"fragments":[{"text":"..."}]}`)},
		)
	}
	// Fallback: the whole unit as a single fragment (old behavior). Never errors.
	fb := fallback
	if fb == "" {
		fb = strings.TrimSpace(last)
	}
	return SegResult{ContinuesPrevious: false, Fragments: []Segment{{Text: fb}}}, nil
}

// segPrompt is the segmentation instruction, with the open fragment appended
// when one is carried over.
func segPrompt(openText string) string {
	p := `Segment this document unit into retrieval fragments. Output ONLY a JSON object:
{"continues_previous": <bool>, "fragments": [{"text": "..."}]}

Rules:
- Carry the content faithfully (transcribe an image exactly; keep code verbatim).
- Group into COHERENT fragments of roughly 400-800 words. Bind small related
  units together (e.g. several short functions, a cluster of list items) to reach
  that size. Do NOT emit tiny atomic fragments; a block under ~300 words should
  almost always be merged with an adjacent one. Split only at strong semantic
  boundaries.
- If the FIRST fragment continues the OPEN FRAGMENT below, set continues_previous
  to true and make fragments[0] ONLY the continuation text (do not repeat the
  open fragment). If there is no open fragment, continues_previous must be false.`
	if strings.TrimSpace(openText) != "" {
		p += "\n\nOPEN FRAGMENT (the previous unit ended mid-fragment with):\n" + openText
	} else {
		p += "\n\n(There is no open fragment; continues_previous must be false.)"
	}
	return p
}

// extractJSON pulls the first {...} object out of a model reply, tolerating
// ```json fences and surrounding prose.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return strings.TrimSpace(s[start : end+1])
	}
	return strings.TrimSpace(s)
}

// Assembler stitches per-unit SegResults into finalized fragments, deferring the
// open (last) fragment across units. It calls sink(page, ord, text) for each
// CLOSED fragment; the open fragment is finalized only when a later fragment
// replaces it or Close() is called — so a fragment spanning a page/window break
// is merged, and the open fragment is never embedded prematurely.
// Fragment size floor/ceiling (chars ≈ 6/word). MinChars enforces a ~500-word
// floor by absorbing sub-floor sibling fragments into the open one; MaxChars
// stops that absorption so one fragment can't swallow a whole document. A hit
// below the floor loses its surrounding context (concept-chaining); above the
// ceiling, injection is handled by pointer notifications (fetch on demand), not
// by inlining the body — so no summarization pass is needed.
const (
	defaultMinFragmentChars = 3000 // ~500 words
	defaultMaxFragmentChars = 9000 // ~1500 words
)

type Assembler struct {
	sink func(page, ord int, text string) error
	open *openFragment
	ord  map[int]int
	// MinChars: absorb sub-floor siblings up to this size (0 disables the floor).
	// MaxChars: never absorb past this.
	MinChars, MaxChars int
}

type openFragment struct {
	text      string
	page, ord int
}

// NewAssembler builds an Assembler; sink finalizes a closed fragment
// (e.g. insert row + hand to the embed pipeline).
func NewAssembler(sink func(page, ord int, text string) error) *Assembler {
	return &Assembler{
		sink:     sink,
		ord:      map[int]int{},
		MinChars: defaultMinFragmentChars,
		MaxChars: defaultMaxFragmentChars,
	}
}

func (a *Assembler) nextOrd(page int) int {
	o := a.ord[page]
	a.ord[page] = o + 1
	return o
}

// OpenText is the current open (deferred) fragment's text, or "" — passed to the
// next unit's segmentation as continuation context.
func (a *Assembler) OpenText() string {
	if a.open != nil {
		return a.open.text
	}
	return ""
}

// Feed processes one unit's segmentation. page is the unit's page number (0 for
// text windows, or a running window index).
func (a *Assembler) Feed(page int, r SegResult) error {
	for i, f := range r.Fragments {
		text := strings.TrimSpace(f.Text)
		if text == "" {
			continue
		}
		if a.open == nil {
			a.open = &openFragment{text: text, page: page, ord: a.nextOrd(page)}
			continue
		}
		// Continuation: the model says this first fragment continues the open one
		// (a mid-fragment span across the unit boundary). It keeps the open
		// fragment's start page/ord.
		if i == 0 && r.ContinuesPrevious {
			a.open.text += "\n\n" + text
			continue
		}
		// Size floor: absorb a sub-floor sibling instead of emitting a tiny
		// fragment, as long as we stay under the ceiling.
		if a.MinChars > 0 && len(a.open.text) < a.MinChars && len(a.open.text)+len(text) <= a.MaxChars {
			a.open.text += "\n\n" + text
			continue
		}
		// The open fragment clears the floor (or absorbing would overflow) → close it.
		if err := a.sink(a.open.page, a.open.ord, a.open.text); err != nil {
			return err
		}
		a.open = &openFragment{text: text, page: page, ord: a.nextOrd(page)}
	}
	return nil
}

// Close finalizes the last open fragment (end of document).
func (a *Assembler) Close() error {
	if a.open != nil {
		if err := a.sink(a.open.page, a.open.ord, a.open.text); err != nil {
			return err
		}
		a.open = nil
	}
	return nil
}
