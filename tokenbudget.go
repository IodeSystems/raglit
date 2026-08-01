package raglit

import (
	"context"
	"sync"
	"unicode/utf8"
)

// Sizing a request in the unit the endpoint actually counts.
//
// The limit is in TOKENS. Everything raglit measures is in characters, and the
// conversion between them is not a constant. Measured on one corpus with the
// model's own tokenizer: 4.66 characters per token on legal prose, 1.16 on a
// surveyor's metes and bounds — `N88°14'32"E 147.03'` is nearly one token per
// character, and this corpus is full of them. A single ratio is four times too
// tight for the first document and past the limit for the second, and only the
// second loses anything.
//
// So: count exactly when the endpoint has a tokenizer, and when it does not,
// estimate from a ratio LEARNED from the text at hand rather than assumed. The
// old constant claimed 2 characters per token as a worst case; a survey legal
// description in this corpus is 1.16, so it was not a bound at all.

// TokenCounter counts tokens as the serving model does. Implemented by
// *llm.Client; ok=false means this endpoint cannot count, which is a fact to
// work around rather than an error.
type TokenCounter interface {
	CountTokens(ctx context.Context, text string) (int, bool)
}

// tokenSafety is how much of the measured ratio to trust when estimating.
//
// Only used on the fallback path, where the ratio comes from a SAMPLE and the
// rest of the document may be denser — a page of prose followed by a page of
// coordinates. Being 20% conservative costs an extra request on a long page;
// being optimistic costs the document.
const tokenSafety = 0.8

// charsPerTokenFloor is the ratio used before anything has been measured.
//
// One character per token: the true worst case for text this corpus contains,
// and the number the old constant of 2 should have been. It only governs the
// first estimate on an endpoint with no tokenizer; the first real measurement
// replaces it.
const charsPerTokenFloor = 1.0

// TokenBudget sizes text against a token limit for one document.
//
// Exact where it can be: with a tokenizer, a prefix is measured rather than
// predicted, and the learned ratio only chooses where to start looking so the
// search converges in a call or two instead of a binary search.
type TokenBudget struct {
	counter TokenCounter
	limit   int // tokens; 0 = unknown, no cap

	mu       sync.Mutex
	chars    int // characters measured so far
	tokens   int // tokens they became
	exact    bool
	exactSet bool
}

// NewTokenBudget builds a budget over an optional counter. A nil counter, or a
// limit of 0, yields a budget that never cuts.
func NewTokenBudget(ctx context.Context, counter TokenCounter, limitTokens int) *TokenBudget {
	b := &TokenBudget{counter: counter, limit: limitTokens}
	if counter != nil && limitTokens > 0 {
		// One probe, so later calls know whether to measure or estimate without
		// discovering it per request.
		_, ok := counter.CountTokens(ctx, "probe")
		b.exact, b.exactSet = ok, true
	}
	return b
}

// Unlimited reports whether this budget will never cut.
func (b *TokenBudget) Unlimited() bool { return b == nil || b.limit <= 0 }

// ratio is the observed characters per token, or the floor before anything has
// been observed.
func (b *TokenBudget) ratio() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens <= 0 {
		return charsPerTokenFloor
	}
	return float64(b.chars) / float64(b.tokens)
}

// observe records a measurement, so the estimate for the next unit comes from
// this document rather than from an assumption about documents in general.
func (b *TokenBudget) observe(chars, tokens int) {
	if chars <= 0 || tokens <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chars += chars
	b.tokens += tokens
}

// Tokens counts text, exactly when possible and by the learned ratio otherwise.
func (b *TokenBudget) Tokens(ctx context.Context, text string) int {
	if text == "" {
		return 0
	}
	if b.exact {
		if n, ok := b.counter.CountTokens(ctx, text); ok {
			b.observe(len(text), n)
			return n
		}
	}
	r := b.ratio() * tokenSafety
	if r < 0.1 {
		r = 0.1
	}
	return int(float64(len(text))/r) + 1
}

// Fit returns how many characters of `rest` can be sent alongside `overhead`
// characters of prompt without exceeding the limit — len(rest) when it all fits.
//
// With a tokenizer this MEASURES rather than predicts: the ratio picks a
// starting guess and each check corrects it, so a page of prose and a page of
// coordinates both converge in a call or two. The alternative — one ratio for
// the whole corpus — is what put nine documents past the limit and cut the rest
// to a fifth of what the endpoint would have taken.
//
// minTake guarantees progress: when the overhead alone leaves no room, cutting
// to fit is not available and a zero-length take would never terminate.
func (b *TokenBudget) Fit(ctx context.Context, overhead, rest string, minTake int) int {
	if b.Unlimited() || rest == "" {
		return len(rest)
	}
	over := b.Tokens(ctx, overhead)
	room := b.limit - over
	if room <= 0 {
		// The prompt alone is at the limit. Send the floor and let the endpoint
		// answer: a refusal naming its own limit beats a loop.
		return min(minTake, len(rest))
	}
	if b.Tokens(ctx, rest) <= room {
		return len(rest)
	}
	// Start from the ratio, then correct against measurement. Each pass shrinks
	// by the proportion it was over, which converges fast because the error is
	// multiplicative — a piece 1.6× over the budget is cut by 1.6.
	take := int(float64(room) * b.ratio())
	for pass := 0; pass < 4; pass++ {
		if take >= len(rest) {
			take = len(rest)
		}
		if take < minTake {
			take = min(minTake, len(rest))
		}
		take = runeBoundary(rest, take)
		n := b.Tokens(ctx, rest[:take])
		if n <= room {
			return take
		}
		if take <= minTake {
			// Cannot shrink further without stalling.
			return take
		}
		take = int(float64(take) * float64(room) / float64(n) * 0.95)
	}
	if take < minTake {
		take = min(minTake, len(rest))
	}
	return runeBoundary(rest, take)
}

// FitSuffix returns how many characters of the END of `s` fit in `room` tokens.
//
// The mirror of Fit, and it exists because the two cuts protect different
// things. A take keeps the FRONT of the remaining page, because that is what
// comes next. A carried-over fragment keeps its TAIL, because continuation is
// decided by how it ends.
func (b *TokenBudget) FitSuffix(ctx context.Context, s string, room int) int {
	if room <= 0 || s == "" {
		return 0
	}
	if b.Tokens(ctx, s) <= room {
		return len(s)
	}
	keep := int(float64(room) * b.ratio())
	for pass := 0; pass < 4; pass++ {
		if keep >= len(s) {
			keep = len(s)
		}
		if keep <= 0 {
			return 0
		}
		keep = suffixRuneBoundary(s, keep)
		n := b.Tokens(ctx, s[len(s)-keep:])
		if n <= room {
			return keep
		}
		keep = int(float64(keep) * float64(room) / float64(n) * 0.95)
	}
	if keep <= 0 {
		return 0
	}
	return suffixRuneBoundary(s, keep)
}

// suffixRuneBoundary shrinks a suffix length until it starts on a rune boundary.
func suffixRuneBoundary(s string, keep int) int {
	if keep >= len(s) {
		return len(s)
	}
	for keep > 0 && !utf8.RuneStart(s[len(s)-keep]) {
		keep--
	}
	return keep
}

// runeBoundary moves n back to a rune start, so a cut never splits a character
// into bytes the model reads as U+FFFD.
func runeBoundary(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	if n == 0 {
		return len(s)
	}
	return n
}
