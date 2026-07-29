package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/iodesystems/agentkit/agent"
	"github.com/iodesystems/agentkit/llm"
)

// LLM-driven fragmentation — the llm-seg path, used ONLY for a document a VLM
// already transcribed (see pipeline.go's per-document choice). Text takes the
// deterministic windower (fragment.go) instead.
//
// The model READS a unit's text (a VLM-OCR'd page) and returns coherent retrieval
// fragments PLUS whether the first continues an "open" fragment carried over from
// the previous unit.
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
	MaxRetries int // JSON fix-loop attempts after the first try (default 2)

	// MaxTokens caps one unit's segmentation; 0 → maxTokensFor(the unit), i.e.
	// scaled to the input, since the answer is the input re-emitted. See chat.go.
	MaxTokens int

	validator *agent.SchemaValidator
}

// NewSegmenter builds a Segmenter over a chat client (an *llm.Client).
func NewSegmenter(c Chatter) *Segmenter {
	return &Segmenter{
		Client:     c,
		MaxRetries: 2,
		validator:  agent.NewSchemaValidator([]llm.ToolDef{fragmentsToolDef()}),
	}
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
	// The answer is the prompt's content re-emitted, so the prompt sizes the cap.
	maxTok := sg.MaxTokens
	if maxTok <= 0 {
		var in string
		for _, p := range parts {
			in += p.Text
		}
		maxTok = maxTokensFor(in)
	}
	opts := &llm.ChatOpts{MaxTokens: maxTok}
	var last string
	var lastErr error
	for attempt := 0; attempt <= sg.MaxRetries; attempt++ {
		out, rep, err := collectStream(ctx, sg.Client, msgs, opts)
		if err != nil {
			return SegResult{}, err // infrastructure failure → propagate (job fails)
		}
		last = out
		if rep != nil {
			// A cut generation cannot hold complete JSON, so skip validation and
			// say WHY in the re-prompt. Naming the repetition matters: the fix
			// loop's only lever is the context, and at temperature 0 a retry that
			// says nothing new reproduces the loop token for token.
			lastErr = fmt.Errorf("you %s", rep)
			// Change the SAMPLER as well as the prompt. The re-prompt alone can
			// break the tie, but it does not have to: at --temp 0 the model is
			// free to reproduce the loop despite the new instruction.
			opts = loopBreakSampling(opts)
			msgs = append(msgs,
				llm.Message{Role: "assistant", Content: excerptForRetry(out)},
				llm.Message{Role: "user", Content: fmt.Sprintf(
					"Your answer was cut off: %v. Do NOT repeat any block of text. "+
						"Emit each fragment once and output ONLY the JSON object %s.",
					lastErr, `{"continues_previous":<bool>,"fragments":[{"text":"..."}]}`)},
			)
			continue
		}
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
			llm.Message{Role: "assistant", Content: excerptForRetry(out)},
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

// retryExcerptChars bounds what a re-prompt quotes back at the model.
//
// Three attempts, each appending the previous answer IN FULL, is unbounded
// growth on exactly the input that triggers it: a cut generation is cut because
// it ran long, and `maxTokensFor` scales that cap to the window, so the failing
// answer is the biggest one. Measured on a live corpus, twelve jobs died at
// "request (180273 tokens) exceeds the available context size (180224)" — the
// fix loop, not the document, filled the context.
//
// A bounded excerpt keeps what the re-prompt is for. The model needs to see
// THAT its answer was malformed and roughly how; it does not need its own
// twenty thousand tokens of repetition read back to it. This also brings the
// segmenter in line with the OCR path, which deliberately does not re-anchor a
// retry on a bad generation.
const retryExcerptChars = 800

// excerptForRetry is the head of a failed answer, marked when truncated.
func excerptForRetry(s string) string {
	if len(s) <= retryExcerptChars {
		return s
	}
	return s[:retryExcerptChars] + "\n…[truncated: the rest of this answer is not repeated back to you]"
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

// PageSpan marks where a page's content begins inside a fragment's text.
//
// A fragment spans pages whenever the assembler absorbs a continuation, or a
// sub-floor sibling, from the following page. Keeping only the start page made
// `fragments.page` right for the beginning of a fragment and wrong for the rest,
// so a hit inside one could not be resolved to the page it actually sits on.
type PageSpan struct {
	Off  int `json:"off"`  // byte offset into the fragment's text
	Page int `json:"page"` // the page whose content begins there
}

type Assembler struct {
	sink func(page, ord int, text string, spans []PageSpan) error
	open *openFragment
	ord  map[int]int
	// MinChars: absorb sub-floor siblings up to this size (0 disables the floor).
	// MaxChars: never absorb past this.
	MinChars, MaxChars int
}

type openFragment struct {
	text      string
	page, ord int
	spans     []PageSpan // always starts with {0, page}
}

// absorb appends text from `page`, recording a boundary when the page changes.
func (o *openFragment) absorb(sep, text string, page int) {
	o.text += sep
	if len(o.spans) == 0 || o.spans[len(o.spans)-1].Page != page {
		o.spans = append(o.spans, PageSpan{Off: len(o.text), Page: page})
	}
	o.text += text
}

// spansOf returns the boundaries worth persisting: nil when the fragment lies
// entirely on one page, so the common case costs nothing.
func spansOf(o *openFragment) []PageSpan {
	if len(o.spans) < 2 {
		return nil
	}
	return o.spans
}

// NewAssembler builds an Assembler; sink finalizes a closed fragment
// (e.g. insert row + hand to the embed pipeline).
func NewAssembler(sink func(page, ord int, text string, spans []PageSpan) error) *Assembler {
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
			a.open = &openFragment{text: text, page: page, ord: a.nextOrd(page),
				spans: []PageSpan{{Off: 0, Page: page}}}
			continue
		}
		// Continuation: the model says this first fragment continues the open one
		// (a mid-fragment span across the unit boundary). It keeps the open
		// fragment's start page/ord.
		if i == 0 && r.ContinuesPrevious {
			a.open.absorb("\n\n", text, page)
			continue
		}
		// Size floor: absorb a sub-floor sibling instead of emitting a tiny
		// fragment, as long as we stay under the ceiling.
		if a.MinChars > 0 && len(a.open.text) < a.MinChars && len(a.open.text)+len(text) <= a.MaxChars {
			a.open.absorb("\n\n", text, page)
			continue
		}
		// The open fragment clears the floor (or absorbing would overflow) → close it.
		if err := a.sink(a.open.page, a.open.ord, a.open.text, spansOf(a.open)); err != nil {
			return err
		}
		a.open = &openFragment{text: text, page: page, ord: a.nextOrd(page),
			spans: []PageSpan{{Off: 0, Page: page}}}
	}
	return nil
}

// Close finalizes the last open fragment (end of document).
func (a *Assembler) Close() error {
	if a.open != nil {
		if err := a.sink(a.open.page, a.open.ord, a.open.text, spansOf(a.open)); err != nil {
			return err
		}
		a.open = nil
	}
	return nil
}

// SplitOversized bounds fragments the model returned, so a fragment can never be
// larger than the embedding model will take as one input.
//
// The deterministic windower is already capped by ResolveFragParams. The LLM
// path was not: it asks for "roughly 400-800 words" and takes whatever comes
// back, so a dense page could yield a fragment no embedder would accept. The
// failure was silent in the worst way — the fragment reached the API, the API
// returned a 500, and the whole document failed with a message about batch
// sizes.
//
// Splitting rather than TRUNCATING is the point. A truncated fragment is
// indexed, searchable, and quietly missing its tail, which is the same class of
// failure as a transcription that reads complete. A split fragment keeps every
// character; only the boundary is arbitrary.
//
// Deterministic, and that is what makes it safe to run after the model: a fixed
// window cannot propose another oversized piece, so this cannot loop.
func SplitOversized(frags []Segment, limitChars int) []Segment {
	if limitChars <= 0 {
		return frags
	}
	out := make([]Segment, 0, len(frags))
	for _, f := range frags {
		if len(f.Text) <= limitChars {
			out = append(out, f)
			continue
		}
		for _, piece := range splitAtBoundary(f.Text, limitChars) {
			out = append(out, Segment{Text: piece})
		}
	}
	return out
}

// splitAtBoundary cuts s into pieces of at most limit characters, preferring a
// paragraph break, then a sentence end, then whitespace — so a piece rarely ends
// mid-word and never ends mid-character.
func splitAtBoundary(s string, limit int) []string {
	var out []string
	for len(s) > limit {
		cut := -1
		// Look for a boundary in the last third of the window: earlier than that
		// and the pieces get needlessly small.
		lo := limit * 2 / 3
		for _, sep := range []string{"\n\n", ". ", ".\n", "\n", " "} {
			if i := strings.LastIndex(s[lo:limit], sep); i >= 0 {
				cut = lo + i + len(sep)
				break
			}
		}
		if cut <= 0 {
			// No boundary at all — a single unbroken run. Cut on a rune boundary
			// so the piece stays valid UTF-8.
			cut = limit
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
			if cut == 0 {
				cut = limit
			}
		}
		out = append(out, strings.TrimSpace(s[:cut]))
		s = s[cut:]
	}
	if t := strings.TrimSpace(s); t != "" {
		out = append(out, t)
	}
	return out
}
