package raglit

import (
	"context"
	"strings"
	"testing"
)

// fakeTokenizer counts tokens at a fixed density, standing in for a model's
// tokenizer. The densities used below are the ones measured against the real
// endpoint with its own tokenizer: 4.66 characters per token on legal prose,
// 1.16 on a survey's metes and bounds.
type fakeTokenizer struct {
	charsPerToken float64
	absent        bool
}

func (f *fakeTokenizer) CountTokens(_ context.Context, text string) (int, bool) {
	if f.absent {
		return 0, false
	}
	return int(float64(len(text))/f.charsPerToken) + 1, true
}

// endpointWithTokenLimit refuses any request over limitTokens, the way the
// endpoint did. It records the token size of every request it served.
func endpointWithTokenLimit(limitTokens int, tok *fakeTokenizer, sizes *[]int) func(context.Context, string, string) (SegResult, error) {
	return func(ctx context.Context, text, open string) (SegResult, error) {
		n, _ := tok.CountTokens(ctx, segPrompt(open)+segContentHeader+text)
		*sizes = append(*sizes, n)
		if n > limitTokens {
			return SegResult{}, &inputTooLargeStub{}
		}
		return SegResult{Fragments: []Segment{{Text: text}}}, nil
	}
}

type inputTooLargeStub struct{}

func (e *inputTooLargeStub) Error() string {
	return "input is too large to process. increase the physical batch size"
}

// The failure that lost nine documents: a page larger than the endpoint's
// accepted input was sent whole, refused with a 500 that is correctly never
// retried, and took the whole document with it.
func TestSegmentInputIsCappedInTokens(t *testing.T) {
	const limit = 2000
	tok := &fakeTokenizer{charsPerToken: 4.66}
	page := strings.Repeat("The parties agree that the easement runs with the land. ", 800)
	pages := []resolvedPage{{page: 1, text: page}}

	var sizes []int
	var got []stagedFrag
	budget := NewTokenBudget(context.Background(), tok, limit)
	_, err := segmentLLMWith(context.Background(),
		endpointWithTokenLimit(limit, tok, &sizes), pages, nil, budget,
		func(f stagedFrag) { got = append(got, f) })
	if err != nil {
		t.Fatalf("a page over the endpoint's limit still failed the document: %v", err)
	}
	if len(sizes) < 2 {
		t.Fatalf("page was sent in %d request(s); it cannot fit in one", len(sizes))
	}
	for i, n := range sizes {
		if n > limit {
			t.Fatalf("request %d was %d tokens, over the endpoint's %d", i, n, limit)
		}
	}
	if in, out := contentChars(page), totalContent(got); out < in {
		t.Fatalf("cutting the request lost text: %d content chars in, %d out", in, out)
	}
}

// The point of counting rather than converting: the SAME character length is a
// different number of tokens for prose and for a survey legal description, and a
// cap that cannot tell them apart is wrong for both. Dense text has to yield
// more, smaller sub-units — not one oversized request.
func TestSegmentCapAdaptsToTextDensity(t *testing.T) {
	const limit = 2000
	body := strings.Repeat("x", 40000)
	run := func(charsPerToken float64) (requests, maxSize int) {
		tok := &fakeTokenizer{charsPerToken: charsPerToken}
		var sizes []int
		budget := NewTokenBudget(context.Background(), tok, limit)
		pages := []resolvedPage{{page: 1, text: body}}
		if _, err := segmentLLMWith(context.Background(),
			endpointWithTokenLimit(limit, tok, &sizes), pages, nil, budget,
			func(stagedFrag) {}); err != nil {
			t.Fatalf("density %.2f: %v", charsPerToken, err)
		}
		for _, n := range sizes {
			if n > maxSize {
				maxSize = n
			}
		}
		return len(sizes), maxSize
	}

	proseReqs, proseMax := run(4.66)
	surveyReqs, surveyMax := run(1.16)
	if proseMax > limit || surveyMax > limit {
		t.Fatalf("a request exceeded the limit: prose %d, survey %d, limit %d", proseMax, surveyMax, limit)
	}
	// Same characters, four times the tokens → more requests, not one refusal.
	if surveyReqs <= proseReqs {
		t.Fatalf("dense text was not cut more finely: prose %d requests, survey %d", proseReqs, surveyReqs)
	}
}

// Without a tokenizer the budget estimates, and the estimate must still hold. It
// starts at one character per token — the true worst case for this corpus, and
// what the old constant of 2 wrongly claimed to be.
func TestSegmentCapHoldsWithoutATokenizer(t *testing.T) {
	const limit = 2000
	blind := &fakeTokenizer{absent: true}
	server := &fakeTokenizer{charsPerToken: 1.16} // the endpoint still counts

	var sizes []int
	budget := NewTokenBudget(context.Background(), blind, limit)
	pages := []resolvedPage{{page: 1, text: strings.Repeat("N88°14'32\"E 147.03', ", 900)}}
	if _, err := segmentLLMWith(context.Background(),
		endpointWithTokenLimit(limit, server, &sizes), pages, nil, budget,
		func(stagedFrag) {}); err != nil {
		t.Fatalf("estimated sizing failed the document: %v", err)
	}
	for i, n := range sizes {
		if n > limit {
			t.Fatalf("estimated request %d was %d tokens, over %d", i, n, limit)
		}
	}
}

// An unknown limit must not cut. An invented cap splits pages that were fine.
func TestSegmentUncappedSendsWholePages(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 4.66}
	var sizes []int
	pages := []resolvedPage{{page: 1, text: strings.Repeat("Paragraph of the agreement. ", 500)}}
	budget := NewTokenBudget(context.Background(), tok, 0)
	if _, err := segmentLLMWith(context.Background(),
		endpointWithTokenLimit(1<<30, tok, &sizes), pages, nil, budget,
		func(stagedFrag) {}); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 1 {
		t.Fatalf("an unknown limit split the page into %d requests", len(sizes))
	}
}

// Overhead alone past the limit must still advance, or the take loop spins
// forever on a document it can never send.
func TestTokenBudgetAlwaysAdvances(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 1.0}
	b := NewTokenBudget(context.Background(), tok, 50)
	if n := b.Fit(context.Background(), strings.Repeat("x", 5000), strings.Repeat("y", 10000), segmentMinTake); n <= 0 {
		t.Fatalf("Fit = %d with overhead past the limit — the loop would not terminate", n)
	}
}

// The ratio has to come from the text at hand, so a budget that has measured
// prose does not size a survey page as though it were prose.
func TestTokenBudgetLearnsFromWhatItMeasured(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 4.66}
	b := NewTokenBudget(context.Background(), tok, 10000)
	b.Tokens(context.Background(), strings.Repeat("prose text here. ", 500))
	if r := b.ratio(); r < 4.0 || r > 5.5 {
		t.Fatalf("ratio = %.2f after measuring 4.66-density text", r)
	}
}

// The carried-over fragment is trimmed from the FRONT: continuation is decided
// by how it ends, so the tail is the part worth keeping.
func TestOpenFragmentTrimKeepsTheTail(t *testing.T) {
	tok := &fakeTokenizer{charsPerToken: 1.0}
	b := NewTokenBudget(context.Background(), tok, 3000)
	open := strings.Repeat("head ", 400) + "THE-TAIL"
	got := segmentOpenForRequest(context.Background(), open, b)
	if got == "" {
		t.Skip("trimmed to nothing; prompt overhead dominates at this limit")
	}
	if !strings.HasSuffix(got, "THE-TAIL") {
		t.Fatalf("trim dropped the tail, which is the part continuation depends on: %q", got[max(0, len(got)-40):])
	}
}

func totalContent(frags []stagedFrag) int {
	n := 0
	for _, f := range frags {
		n += contentChars(f.text)
	}
	return n
}
