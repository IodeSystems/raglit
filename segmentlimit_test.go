package raglit

import (
	"context"
	"strings"
	"testing"
)

// endpointWithLimit is a segmenter standing in for a chat endpoint that refuses
// any request over limitChars — llama.cpp's physical batch, which is what the
// real one did. It records every request size it was asked to serve.
func endpointWithLimit(limitChars int, sizes *[]int) func(context.Context, string, string) (SegResult, error) {
	return func(_ context.Context, text, open string) (SegResult, error) {
		n := SegmentInputChars(text, open)
		*sizes = append(*sizes, n)
		if n > limitChars {
			return SegResult{}, &InputTooLargeStub{Size: n, Limit: limitChars}
		}
		return SegResult{Fragments: []Segment{{Text: text}}}, nil
	}
}

// InputTooLargeStub stands in for the endpoint's refusal.
type InputTooLargeStub struct{ Size, Limit int }

func (e *InputTooLargeStub) Error() string {
	return "input is too large to process. increase the physical batch size"
}

// TestSegmentInputIsCappedByTheEndpoint is the failure that lost nine documents
// from a live index: a page larger than the endpoint's accepted input was sent
// whole, refused with a 500 that is correctly never retried, and took the whole
// document with it. Nothing between the page and the request measured anything.
func TestSegmentInputIsCappedByTheEndpoint(t *testing.T) {
	const limit = 6000
	// One page well past the limit — 40k characters of prose.
	page := strings.Repeat("The parties agree that the easement runs with the land. ", 720)
	pages := []resolvedPage{{page: 1, text: page}}

	var sizes []int
	var got []stagedFrag
	_, err := segmentLLMWith(context.Background(), endpointWithLimit(limit, &sizes),
		pages, 4000, limit, func(f stagedFrag) { got = append(got, f) })
	if err != nil {
		t.Fatalf("a page over the endpoint's limit still failed the document: %v", err)
	}
	if len(sizes) < 2 {
		t.Fatalf("page was sent in %d request(s); it cannot fit in one", len(sizes))
	}
	for i, n := range sizes {
		if n > limit {
			t.Fatalf("request %d was %d chars, over the endpoint's %d", i, n, limit)
		}
	}
	// Every character still reaches a fragment: cutting the REQUEST must not cut
	// the document. Compared on content chars, since the assembler trims.
	if in, out := contentChars(page), totalContent(got); out < in {
		t.Fatalf("cutting the request lost text: %d content chars in, %d out", in, out)
	}
}

// An unknown limit (unprobeable endpoint) must not cut. An invented cap splits
// pages that were fine, and the failure it would guard against is loud.
func TestSegmentUncappedSendsWholePages(t *testing.T) {
	page := strings.Repeat("Paragraph of the agreement. ", 500)
	pages := []resolvedPage{{page: 1, text: page}}
	var sizes []int
	_, err := segmentLLMWith(context.Background(), endpointWithLimit(1<<30, &sizes),
		pages, 4000, 0, func(stagedFrag) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 1 {
		t.Fatalf("an unknown limit split the page into %d requests", len(sizes))
	}
}

// The open fragment is part of the request and runs to defaultMaxFragmentChars.
// A take sized against the page text alone is short by that much and is still
// refused — which is the bug this guards, not a hypothetical.
func TestSegmentTakeCountsThePromptAndOpenFragment(t *testing.T) {
	const limit = 8000
	open := strings.Repeat("x", 5000)
	rest := strings.Repeat("y", 20000)
	take := segmentTake(rest, open, limit)
	if take <= 0 || take > len(rest) {
		t.Fatalf("take = %d, out of range for a %d-char remainder", take, len(rest))
	}
	if n := SegmentInputChars(rest[:take], open); n > limit {
		t.Fatalf("take of %d yields a %d-char request, over the %d limit", take, n, limit)
	}
}

// Overhead alone at or over the limit must still advance, or the take loop spins
// forever on a document it can never send.
func TestSegmentTakeAlwaysAdvances(t *testing.T) {
	open := strings.Repeat("x", 50000)
	rest := strings.Repeat("y", 10000)
	if take := segmentTake(rest, open, 100); take <= 0 {
		t.Fatalf("take = %d with overhead past the limit — the loop would not terminate", take)
	}
}

func totalContent(frags []stagedFrag) int {
	n := 0
	for _, f := range frags {
		n += contentChars(f.text)
	}
	return n
}
