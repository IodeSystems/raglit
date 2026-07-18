package raglit

import (
	"context"
	"strings"
)

// Text/code windowing for segmentation.
//
// A file too big for the model's context is fed in ordered WINDOWS; the
// Assembler's cross-unit continuation stitches any fragment split at a window
// boundary, so windows don't need clean semantic cuts. The model ECHOES the text
// back as segmented fragments, so input AND output both live in the context
// window → usable input ≈ (ctx − prompt) / 2.
//
// The window size comes from config (ContextTokens, set in the wizard) or a
// SMART DEFAULT — not a per-ingest probe. Because maxWindowChars caps the window
// for output reliability, any context ≥ ~40k tokens yields the SAME window, so
// the exact number rarely matters; the probe (llm.DiscoverContext, an opt-in
// wizard step) is only useful for a genuinely small-context model.

const (
	segPromptOverheadTokens = 400    // ~the segmentation instruction + JSON scaffolding
	defaultContextTokens    = 131072 // smart default when config leaves it unset (see above)
	maxWindowChars          = 64000  // ~16k tokens. NOT a context or latency limit (a large
	//                                 context — bonsai is 256k — leaves ample headroom, and
	//                                 total latency is dominated by output tokens, ~constant
	//                                 across window sizes). It bounds how much a SMALL model
	//                                 is asked to re-emit as one structured blob before it
	//                                 drifts/repeats. Most files fit in one window at this
	//                                 size → better coherence than boundary-stitching.
	charsPerToken = 4
)

// WindowCharsFor turns a context-token count into a window size in CHARACTERS,
// budgeting for the prompt and the model's echoed output (÷2, since segmentation
// re-emits the text), with a 20% safety margin — then clamped to maxWindowChars
// (an output-reliability bound, not a context bound). Exported so a caller with
// a known context (e.g. --context-tokens) can size windows without probing.
func WindowCharsFor(ctxTokens int) int {
	usable := (ctxTokens - segPromptOverheadTokens) / 2
	if usable < 256 {
		usable = 256
	}
	usable = usable * 8 / 10
	chars := usable * charsPerToken
	if chars > maxWindowChars {
		chars = maxWindowChars
	}
	return chars
}

// WindowCharsForHome returns the text window size (chars) for a home: its
// configured ContextTokens if set, else the smart default. No probing — set the
// context in the wizard (or --context-tokens) for a small-context model.
func WindowCharsForHome(home Home) int {
	cfg, _, _ := LoadConfig(home)
	c := cfg.ContextTokens
	if c <= 0 {
		c = defaultContextTokens
	}
	return WindowCharsFor(c)
}

// textWindows splits text into windows of at most maxChars, breaking at line
// boundaries (a single over-long line is emitted alone). maxChars <= 0 or text
// that already fits yields one window.
func textWindows(text string, maxChars int) []string {
	if maxChars <= 0 || len(text) <= maxChars {
		return []string{text}
	}
	var windows []string
	var b strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if b.Len() > 0 && b.Len()+len(line) > maxChars {
			windows = append(windows, b.String())
			b.Reset()
		}
		b.WriteString(line)
		if b.Len() >= maxChars {
			windows = append(windows, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		windows = append(windows, b.String())
	}
	return windows
}

// ingestText segments a text/code document into coherent fragments via the LLM,
// windowing to windowChars (0 → default). Cross-window continuation stitches
// fragments split at a window boundary. Falls under one logical page (0).
func (s *Store) ingestText(ctx context.Context, sg *Segmenter, docPath, title, text string, windowChars int) (int, error) {
	windows := textWindows(text, windowChars)
	units := make([]ingestUnit, len(windows))
	for i, w := range windows {
		units[i] = ingestUnit{page: 0, text: w}
	}
	return s.ingestUnits(ctx, sg, docPath, title, units)
}
