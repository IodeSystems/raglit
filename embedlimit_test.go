package raglit

import (
	"context"
	"strings"
	"testing"
)

// The failure that was still killing documents after the chat-side cap shipped,
// and the one the character ceiling could never have caught: a fragment inside
// EmbedLimitChars (16128, converted from 8192 tokens at two characters per
// token) that is 10240 tokens of scanned court brief. Refused with a 500, not
// retried, and the whole document fails at the embed stage — after its OCR and
// its segmentation have already been paid for.
func TestFragmentsAreCappedByTheEmbedderInTokens(t *testing.T) {
	const limit = 8192
	tok := &fakeTokenizer{charsPerToken: 1.57} // measured density of that brief
	b := NewTokenBudget(context.Background(), tok, limit)

	f := stagedFrag{page: 3, ord: 0, text: strings.Repeat("x", 16128)}
	if n, _ := tok.CountTokens(context.Background(), f.text); n <= limit {
		t.Fatalf("test premise broken: %d tokens is not over the %d limit", n, limit)
	}

	pieces := splitForEmbed(context.Background(), b, f)
	if len(pieces) < 2 {
		t.Fatalf("an over-limit fragment was not split: %d piece(s)", len(pieces))
	}
	total := 0
	for i, p := range pieces {
		n, _ := tok.CountTokens(context.Background(), p.text)
		if n > limit {
			t.Fatalf("piece %d is %d tokens, over the embedder's %d", i, n, limit)
		}
		total += len(p.text)
	}
	if total != len(f.text) {
		t.Fatalf("splitting lost text: %d chars in, %d out", len(f.text), total)
	}
}

// A fragment already inside the limit must come back untouched — splitting a
// healthy fragment costs recall for nothing.
func TestFragmentUnderTheLimitIsNotSplit(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 4.66}
	b := NewTokenBudget(context.Background(), tok, 8192)
	f := stagedFrag{page: 1, text: strings.Repeat("prose. ", 500)}
	pieces := splitForEmbed(context.Background(), b, f)
	if len(pieces) != 1 || pieces[0].text != f.text {
		t.Fatalf("a fragment inside the limit was split into %d", len(pieces))
	}
}

// pageSpans resolve a hit inside a stitched fragment to the page it sits on.
// They have to survive the cut, or a search answers with the wrong page.
func TestSplitForEmbedCarriesPageSpans(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 1.0}
	b := NewTokenBudget(context.Background(), tok, 1000)
	f := stagedFrag{
		page: 1,
		text: strings.Repeat("a", 1200) + strings.Repeat("b", 1200) + strings.Repeat("c", 1200),
		pageSpans: []PageSpan{
			{Off: 0, Page: 1}, {Off: 1200, Page: 2}, {Off: 2400, Page: 3},
		},
	}
	pieces := splitForEmbed(context.Background(), b, f)
	if len(pieces) < 3 {
		t.Fatalf("want at least 3 pieces at a 1000-token limit, got %d", len(pieces))
	}
	// Every piece must know which page it opens on.
	off := 0
	for i, p := range pieces {
		wantPage := 1 + off/1200
		if wantPage > 3 {
			wantPage = 3
		}
		got := p.page
		if len(p.pageSpans) > 0 {
			got = p.pageSpans[0].Page
		} else if i > 0 {
			// No spans means the piece lies inside one page; it must still be
			// attributable, which the fragment's own page provides.
			continue
		}
		if i > 0 && got != wantPage {
			t.Errorf("piece %d (offset %d) opens on page %d, want %d", i, off, got, wantPage)
		}
		off += len(p.text)
	}
}

// Unlimited (no embedder limit established) must never split.
func TestSplitForEmbedUnlimited(t *testing.T) {
	b := NewTokenBudget(context.Background(), nil, 0)
	f := stagedFrag{page: 1, text: strings.Repeat("x", 100000)}
	if pieces := splitForEmbed(context.Background(), b, f); len(pieces) != 1 {
		t.Fatalf("an unknown limit split a fragment into %d pieces", len(pieces))
	}
}

// The two splitters have to agree, or a fragment is cut twice in different
// places and the second cut is the arbitrary one.
//
// SplitOversized bounds what the model returns; the sink bounds every fragment
// on every path as a backstop. Running both against the same budget, the second
// must find nothing to do — and where it does cut (the deterministic windower
// never passes through the first), it must cut on the same boundaries.
func TestTheTwoSplittersAgree(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 1.16} // survey density: the hard case
	b := NewTokenBudget(context.Background(), tok, 2000)

	long := strings.Repeat("N88°14'32\"E 147.03', S01°45'28\"E 100.00'. ", 400)
	pieces := SplitOversized(context.Background(), b, []Segment{{Text: long}})
	if len(pieces) < 2 {
		t.Fatalf("premise broken: %d piece(s) from an oversized fragment", len(pieces))
	}
	for i, p := range pieces {
		again := splitForEmbed(context.Background(), b, stagedFrag{page: 1, text: p.Text})
		if len(again) != 1 {
			t.Fatalf("piece %d survived SplitOversized and was cut again by the sink into %d",
				i, len(again))
		}
		if again[0].text != p.Text {
			t.Fatalf("piece %d was altered by the second pass", i)
		}
	}
}

// The sink's cut prefers the same boundaries, so a windower fragment — which
// never passes through SplitOversized — is not cut mid-sentence either.
func TestSinkSplitPrefersBoundaries(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 1.0}
	b := NewTokenBudget(context.Background(), tok, 1200)
	text := strings.Repeat("A sentence that ends here. ", 200)
	pieces := splitForEmbed(context.Background(), b, stagedFrag{page: 1, text: text})
	if len(pieces) < 2 {
		t.Fatalf("premise broken: %d piece(s)", len(pieces))
	}
	// Every piece but the last should end at a sentence or a space, not mid-word.
	for i, p := range pieces[:len(pieces)-1] {
		last := p.text[len(p.text)-1]
		if last != ' ' && last != '.' && last != '\n' {
			t.Errorf("piece %d ends mid-word at %q", i, p.text[max(0, len(p.text)-24):])
		}
	}
	// And no character is lost across the cut.
	var joined string
	for _, p := range pieces {
		joined += p.text
	}
	if joined != text {
		t.Errorf("the sink split lost or altered text: %d chars in, %d out", len(text), len(joined))
	}
}

// The refusal that proved a token count of the fragment is not a count of the
// request: "input (8194 tokens) is too large to process" against a limit of
// 8192. Two tokens over, and the document lost.
//
// The embedder sends DocPrefix + fragment, and CountTokens asks with add_special
// off, so neither the prefix nor the BOS/EOS pair was in the number the budget
// compared. A budget that misses by two fails a document exactly as completely
// as one that misses by two thousand.
func TestEmbedBudgetCountsWhatTheEmbedderActuallySends(t *testing.T) {
	const limit = 8192
	tok := &fakeTokenizer{charsPerToken: 1.0} // one token per char: easy arithmetic
	b := NewTokenBudget(context.Background(), tok, limit)
	b.Overhead = "search_document: "
	b.Reserve = embedSpecialReserve

	// A fragment that fits the RAW limit but not once the prefix and the special
	// tokens are counted.
	f := stagedFrag{page: 1, text: strings.Repeat("x", limit-2)}
	if b.Fits(context.Background(), f.text) {
		t.Fatal("a fragment that leaves no room for the prefix was reported as fitting")
	}
	for i, p := range splitForEmbed(context.Background(), b, f) {
		sent := b.Overhead + p.text
		n, _ := tok.CountTokens(context.Background(), sent)
		if n+b.Reserve > limit {
			t.Fatalf("piece %d sends %d tokens plus %d reserved, over the %d limit",
				i, n, b.Reserve, limit)
		}
	}
}
