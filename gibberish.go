package raglit

import (
	"strings"
	"unicode"
)

// GibberishConfig tunes the detector that decides whether a cheap
// PaddleOCR page is trustworthy or needs a vision-model re-OCR.
// Defaults are PRECISION-BIASED: thresholds sit well below clean-
// print norms so only clearly-bad pages (handwriting, degraded
// scans, garbled figures) trip the fallback. The goal is to keep
// the expensive vision calls rare; the cost of a false-negative is
// a slightly noisy page, the cost of a false-positive is a wasted
// Qwen call.
//
// Zero value → all defaults via withDefaults(). Overrides arrive
// through the OCR config (config.OCR.Gibberish) and are carried on
// OCR.Gate; see the cascade in ocr.go.
type GibberishConfig struct {
	// MinMeanConfidence: pages whose mean recognizer confidence is
	// below this fall back. PP-OCR reports >0.9 mean on clean print;
	// 0.55 only catches genuinely struggling pages.
	MinMeanConfidence float64 `json:"min_mean_confidence"`
	// MinWordlikeFraction: fraction of whitespace tokens that must
	// look like words (letters with vowels, or numbers). Below this,
	// the text is mostly symbol-soup → fall back. Only applied once
	// the page has at least MinTokensForLexical tokens (short pages
	// have too few tokens for a stable ratio).
	MinWordlikeFraction float64 `json:"min_wordlike_fraction"`
	// MinTokensForLexical gates the lexical test: pages with fewer
	// tokens than this are judged on confidence alone.
	MinTokensForLexical int `json:"min_tokens_for_lexical"`
	// MaxJunkRuneFraction: fraction of replacement (U+FFFD) /
	// control runes above which the page is junk regardless of
	// confidence. Catches mojibake the recognizer was "confident"
	// about.
	MaxJunkRuneFraction float64 `json:"max_junk_rune_fraction"`
}

func (g GibberishConfig) withDefaults() GibberishConfig {
	if g.MinMeanConfidence <= 0 {
		g.MinMeanConfidence = 0.55
	}
	if g.MinWordlikeFraction <= 0 {
		g.MinWordlikeFraction = 0.35
	}
	if g.MinTokensForLexical <= 0 {
		g.MinTokensForLexical = 8
	}
	if g.MaxJunkRuneFraction <= 0 {
		g.MaxJunkRuneFraction = 0.05
	}
	return g
}

// IsGibberish reports whether a PaddleOCR page should be re-OCR'd by
// the vision model, with a short human-readable reason for tracing.
//
// A zero-box page is NOT gibberish — it's an empty/blank page (or a
// pure figure with no detectable text), which the cascade emits as
// empty rather than paying the vision model for every blank. A
// figure whose labels paddle garbles will instead trip on low
// confidence or the lexical test, which is the case the user cares
// about ("graphs need Qwen").
func (g GibberishConfig) IsGibberish(po PageOCR) (bool, string) {
	g = g.withDefaults()

	if po.BoxCount == 0 {
		return false, "empty-page"
	}

	if junkRuneFraction(po.Text) > g.MaxJunkRuneFraction {
		return true, "junk-runes"
	}

	// Mean confidence is the primary, cheapest signal. The sidecar
	// computes it over every recognized line.
	if po.MeanConfidence > 0 && po.MeanConfidence < g.MinMeanConfidence {
		return true, "low-confidence"
	}

	// Lexical fallback: catches "confident garbage" — paddle is sure
	// of characters that don't form words (common on handwriting and
	// stylized figure text). Skipped on short pages where the ratio
	// is too noisy to trust.
	tokens := strings.Fields(po.Text)
	if len(tokens) >= g.MinTokensForLexical {
		wl := 0
		for _, t := range tokens {
			if wordlike(t) {
				wl++
			}
		}
		frac := float64(wl) / float64(len(tokens))
		if frac < g.MinWordlikeFraction {
			return true, "low-wordlike-fraction"
		}
	}

	return false, ""
}

// junkRuneFraction is the share of replacement / control runes in s
// (excluding ordinary whitespace). High → mojibake / binary noise.
func junkRuneFraction(s string) float64 {
	if s == "" {
		return 0
	}
	var junk, total int
	for _, r := range s {
		total++
		if r == '�' {
			junk++
			continue
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			junk++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(junk) / float64(total)
}

// wordlike reports whether a whitespace token plausibly reads as a
// word or number. After trimming surrounding punctuation: pure-digit
// tokens pass (page numbers, quantities); otherwise the token must
// be majority letters and — beyond length 2 — contain a vowel. This
// is a crude, dictionary-free heuristic; it only needs to separate
// real text from symbol-soup in aggregate, not judge any single
// token correctly.
func wordlike(tok string) bool {
	tok = strings.TrimFunc(tok, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if tok == "" {
		return false
	}
	var letters, digits, vowels, total int
	for _, r := range tok {
		total++
		switch {
		case isVowel(r):
			vowels++
			letters++
		case unicode.IsLetter(r):
			letters++
		case unicode.IsDigit(r):
			digits++
		}
	}
	if total > 24 {
		return false
	}
	// Pure / mostly numeric tokens are fine (e.g. "1,234" → "1234").
	if digits > 0 && letters == 0 {
		return true
	}
	// Majority must be alphanumeric (filters "***", "—|—").
	if (letters+digits)*2 < total {
		return false
	}
	// Real words longer than 2 chars carry a vowel; their absence is
	// a strong gibberish tell ("brqwx", "ttttt").
	if letters >= 3 && vowels == 0 {
		return false
	}
	return true
}

func isVowel(r rune) bool {
	switch unicode.ToLower(r) {
	case 'a', 'e', 'i', 'o', 'u', 'y':
		return true
	}
	return false
}
