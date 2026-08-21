package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
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

	// Degraded names why this unit was NOT segmented by the model, when the fix
	// loop ran out and run() fell back to the whole unit as one fragment. Empty
	// on a real segmentation.
	//
	// It exists because that fallback returns a nil error and a plausible result:
	// the document completes, the page count is right, and the only evidence that
	// a page came back as an undivided block is a reason that used to be dropped
	// on the floor. Not part of the model's answer — `json:"-"` so a model cannot
	// claim it, and so unmarshalling a reply cannot clear it.
	Degraded string `json:"-"`
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
	// Collection is what the corpus owner says about reading THIS collection
	// (Store.IndexHint), appended to the segmentation prompt. Where a fragment
	// boundary falls depends on knowing what the document IS: a lab panel's
	// results are one unit and a work order's line items are another, and
	// nothing on the page says so.
	Collection string

	// FragBudget is the ceiling a fragment must respect — the EMBEDDER's limit,
	// in the tokens it counts in. nil → unchecked.
	//
	// Enforced by asking the model again rather than by cutting, for as long as
	// the fix loop allows: a model re-splitting its own fragment cuts at a
	// heading or a paragraph, and a splitter cuts at whatever falls near the
	// budget.
	//
	// It replaces a MaxFragChars that nothing ever assigned, so this check has
	// been returning "nothing is oversized" for every document ever indexed and
	// the re-prompt it guards has never run once.
	FragBudget *TokenBudget

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

// segContentHeader separates the instructions from the unit being segmented.
const segContentHeader = "\n\nCONTENT:\n"

// SegmentText segments a window of text/code.
func (sg *Segmenter) SegmentText(ctx context.Context, text, openText string) (SegResult, error) {
	parts := []llm.ContentPart{
		llm.TextPart(segPrompt(openText) + HintBlock(sg.Collection) + segContentHeader + text),
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
			} else if over := oversizedFragments(ctx, sg.FragBudget, r.Fragments); len(over) > 0 {
				// The model cannot gauge tokens, and asking it to would be asking
				// for a guess. It CAN act on "this one is 240% of the maximum,
				// cut it into at least three" — a proportion, in the unit it
				// produced. Re-prompting keeps the split on a semantic boundary;
				// the deterministic fallback does not.
				lastErr = oversizeComplaint(over)
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
	// Fallback: the whole unit as a single fragment (old behavior). Never errors —
	// an unsegmented page is still a searchable page, and failing the document
	// over one stubborn unit would lose the other twenty-nine.
	//
	// But it is not a success either, and it used to be reported as one. lastErr
	// holds the only account of what went wrong — the model looped, or emitted
	// unparseable JSON, or no fragments at all, or a fragment far past the embed
	// ceiling — and it was discarded here, so a document could finish "done" with
	// any number of pages returned as undivided blocks and nothing recorded how
	// many or why. Carry the reason out; the caller records it.
	fb := fallback
	if fb == "" {
		fb = strings.TrimSpace(last)
	}
	reason := "the model did not return valid fragments"
	if lastErr != nil {
		reason = lastErr.Error()
	}
	return SegResult{
		ContinuesPrevious: false,
		Fragments:         []Segment{{Text: fb}},
		Degraded:          fmt.Sprintf("%s (after %d attempt(s))", reason, sg.MaxRetries+1),
	}, nil
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

// extractJSON pulls the FIRST balanced {...} object out of a model reply,
// tolerating ```json fences and surrounding prose.
//
// It used to take the first `{` to the LAST `}`, which is a different thing and
// is wrong whenever a reply carries anything after the object it was asked for.
// A model that answers twice — the object, then a second object, or a trailing
// note — produced a span holding both, and `json.Unmarshal` reported
// `invalid character '{' after top-level value` (or `','`, depending on what
// followed). Measured on the delano index 2026-08-19: **290 of 817 identity jobs
// had failed, 239 of them with exactly that**, and nothing surfaced it until the
// activity pane existed. The FDA corpus hit the mirror image — a reply with no
// object at all came back whole, so the error named the first character of the
// prose (`'/' looking for beginning of value`) rather than saying no JSON was
// found.
//
// Balanced means string-aware: a `{` or `}` inside a JSON string is content, not
// structure, and a legal document quoted inside a summary field has braces in it
// often enough to matter.
//
// The retry loop cannot paper over this. A truncated or doubled answer is not
// repetition, so the sampler never changes, and every attempt fails identically
// until the attempts run out — three model calls to arrive at the same message.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if inner, ok := insideFence(s); ok {
		s = inner
	}
	// Prefer the first candidate that actually parses. A reply whose first
	// balanced object is malformed but whose second is fine is not a shape worth
	// designing for, but preferring valid costs nothing and makes the common
	// "prose containing braces, then the real answer" case work.
	var firstBalanced string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		end, ok := matchBrace(s, i)
		if !ok {
			// Unbalanced from here on; nothing later can close either.
			break
		}
		cand := s[i:end]
		if json.Valid([]byte(cand)) {
			return cand
		}
		if firstBalanced == "" {
			firstBalanced = cand
		}
	}
	// Nothing parsed. Returning the first balanced object still hands the caller
	// the thing that was MEANT to be JSON, so the error names what is wrong with
	// it — a raw newline in a string literal, say — instead of quoting the whole
	// reply back and burying it.
	if firstBalanced != "" {
		return firstBalanced
	}
	return s
}

// insideFence returns the contents of the first ```…``` block, if the reply has
// a closed one. An UNCLOSED fence is left alone: the old code stripped the
// opening marker and kept everything after it, which turns a reply that merely
// mentions a fence into a truncated one.
func insideFence(s string) (string, bool) {
	i := strings.Index(s, "```")
	if i < 0 {
		return "", false
	}
	rest := s[i+3:]
	// The info string ("json", "JSON", "") runs to the end of that line.
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		if info := strings.TrimSpace(rest[:nl]); info == "" || !strings.ContainsAny(info, "{}\"") {
			rest = rest[nl+1:]
		}
	}
	j := strings.Index(rest, "```")
	if j < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:j]), true
}

// matchBrace returns the index just past the `}` closing the object that opens
// at start, tracking string literals and their escapes so braces inside a value
// do not count as structure.
func matchBrace(s string, start int) (int, bool) {
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
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
// Bounded in TOKENS by the embedder's own budget. It used to convert the token
// limit to characters at two characters per token and check the result with an
// estimate built on the same ratio — which is not a bound: a survey legal
// description is 1.16 characters per token, so a piece could pass both checks
// and still be half again over the real limit.
func SplitOversized(ctx context.Context, b *TokenBudget, frags []Segment) []Segment {
	if b.Unlimited() {
		return frags
	}
	out := make([]Segment, 0, len(frags))
	for _, f := range frags {
		out = append(out, splitToFit(ctx, b, f.Text)...)
	}
	return out
}

// splitToFit cuts text until every piece is inside the budget, preferring a
// semantic boundary.
//
// Cutting where the sink would cut is the point of doing it here at all. The
// sink enforces the same budget as a backstop for every path, so a piece this
// function leaves oversized gets cut again there — at a rune boundary, in the
// middle of a sentence, discarding the paragraph break this function could have
// used. Two splitters disagreeing about where a fragment ends is worse than
// either alone: the fragment is cut twice, and the second cut is the arbitrary
// one.
//
// Terminates because Fit never returns zero: it takes at least fragMinTake, and
// a piece that small is inside any budget worth having.
func splitToFit(ctx context.Context, b *TokenBudget, text string) []Segment {
	if b.Unlimited() || b.Fits(ctx, text) {
		return []Segment{{Text: text}}
	}
	var out []Segment
	rest := text
	for rest != "" {
		take := b.FitOne(ctx, rest, fragMinTake)
		if take < len(rest) {
			// Back off to a paragraph or sentence end inside what fits. Never
			// forward: that would put the piece back over the budget.
			if cut := cutAtBoundary(rest, take); cut >= fragMinTake {
				take = cut
			}
		}
		if piece := strings.TrimSpace(rest[:take]); piece != "" {
			out = append(out, Segment{Text: piece})
		}
		rest = rest[take:]
	}
	if len(out) == 0 {
		return []Segment{{Text: text}}
	}
	return out
}

// fragMinTake is the smallest piece worth emitting, and the floor that makes the
// split terminate.
const fragMinTake = 500

// cutAtBoundary returns the offset at or before limit where a cut lands on a
// paragraph break, a sentence end, or whitespace — limit itself when there is no
// boundary to prefer, moved back to a rune start so a piece is always valid
// UTF-8.
//
// Shared by every splitter, so a fragment cut twice is cut in the same places
// both times.
func cutAtBoundary(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	// Only in the last third of the window: earlier than that and the pieces get
	// needlessly small.
	lo := limit * 2 / 3
	for _, sep := range []string{"\n\n", ". ", ".\n", "\n", " "} {
		if i := strings.LastIndex(s[lo:limit], sep); i >= 0 {
			return lo + i + len(sep)
		}
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		return limit
	}
	return cut
}

// splitAtBoundary cuts s into pieces of at most limit characters, preferring a
// paragraph break, then a sentence end, then whitespace — so a piece rarely ends
// mid-word and never ends mid-character.
func splitAtBoundary(s string, limit int) []string {
	var out []string
	for len(s) > limit {
		cut := cutAtBoundary(s, limit)
		out = append(out, strings.TrimSpace(s[:cut]))
		s = s[cut:]
	}
	if t := strings.TrimSpace(s); t != "" {
		out = append(out, t)
	}
	return out
}

// segTargetHeadroom is how much of the ceiling the PROMPT asks for.
//
// A model cannot count tokens, so it is asked for words, and words vary. Aiming
// at the ceiling means half the fragments breach it and every document pays for
// a retry; aiming at two thirds means variance lands inside the limit and the
// retry is for genuine outliers.
const segTargetHeadroom = 0.66

// oversizedFragments reports which fragments breached the ceiling, and by how
// much, as a proportion of the budget.
func oversizedFragments(ctx context.Context, b *TokenBudget, frags []Segment) map[int]float64 {
	if b.Unlimited() {
		return nil
	}
	out := map[int]float64{}
	for i, f := range frags {
		// Counted in TOKENS by the embedder's own tokenizer. The character
		// ceiling this used to compare against was the token limit converted at
		// two characters per token, checked with an estimate built on the same
		// ratio — so on a survey legal description at 1.16 it agreed with itself
		// and was wrong by half.
		if !b.Fits(ctx, f.Text) {
			out[i] = float64(b.Tokens(ctx, f.Text)) / float64(b.room(ctx))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// oversizeComplaint phrases the breach as an instruction the model can follow.
//
// A percentage and a piece count, not a token budget: "fragment 2 is 240% of the
// maximum — split it into at least 3" is actionable, where "keep it under 8192
// tokens" asks for an estimate the model has no way to make.
func oversizeComplaint(over map[int]float64) error {
	idx := make([]int, 0, len(over))
	for i := range over {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	parts := make([]string, 0, len(idx))
	for _, i := range idx {
		ratio := over[i]
		pieces := int(math.Ceil(ratio/segTargetHeadroom)) + 1
		parts = append(parts, fmt.Sprintf("fragment %d is %.0f%% of the maximum length — split it into at least %d",
			i+1, ratio*100, pieces))
	}
	return fmt.Errorf("%s. Split at headings or paragraph breaks, keep every word, and do not merge anything else",
		strings.Join(parts, "; "))
}
