package raglit

import (
	"context"
	"strings"
)

// Text/code windowing for segmentation.
//
// A file too big for the model's context is fed in ordered WINDOWS; the
// Assembler's cross-unit continuation stitches any fragment split at a window
// boundary, so windows don't need clean semantic cuts. Two sizing facts drive
// the window:
//
//   - The model ECHOES the text back as segmented fragments, so input AND output
//     both live in the context window → usable input ≈ (ctx − prompt) / 2.
//   - We never know the true context a priori, so we DISCOVER it by probing
//     (llm.DiscoverContext) and cache it — never by compacting to fit.

// contextDiscoverer is the sliver of *llm.Client windowing needs (probe the
// model's context). An interface so it's testable without a network.
type contextDiscoverer interface {
	DiscoverContext(ctx context.Context) (int, error)
}

const (
	segPromptOverheadTokens = 400   // ~the segmentation instruction + JSON scaffolding
	defaultWindowChars      = 8000  // ~2k tokens: a safe window when discovery is unavailable
	maxWindowChars          = 24000 // ~6k tokens: latency cap — a bigger window doesn't buy
	//                                 much and makes each segment call slow (prompt time
	//                                 grows with size), independent of the model's context.
	charsPerToken = 4
)

// windowCharsFor turns a discovered context-token count into a window size in
// CHARACTERS, budgeting for the prompt and the model's echoed output (÷2, since
// segmentation re-emits the text), with a 20% safety margin — then clamped to
// maxWindowChars so a large context doesn't produce slow, unwieldy windows.
func windowCharsFor(ctxTokens int) int {
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

// ResolveWindowChars returns the text window size (chars) for a model, using the
// home's cached ContextTokens when present, else probing d once and caching the
// result. On any probe failure it falls back to defaultWindowChars (nil error) —
// discovery is an optimization, not a hard dependency.
func ResolveWindowChars(ctx context.Context, d contextDiscoverer, home Home) int {
	cfg, _, _ := LoadConfig(home)
	if cfg.ContextTokens > 0 {
		return windowCharsFor(cfg.ContextTokens)
	}
	n, err := d.DiscoverContext(ctx)
	if err != nil || n <= 0 {
		return defaultWindowChars
	}
	cfg.ContextTokens = n
	_ = SaveConfig(home, cfg) // best-effort cache
	return windowCharsFor(n)
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
