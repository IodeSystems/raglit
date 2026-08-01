package raglit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// drops returns a segmenter that answers with NOTHING for the page containing
// marker — the real failure. The model replies, without an error, and the page's
// words are simply not in the reply.
func drops(marker string) func(context.Context, string, string) (SegResult, error) {
	return func(_ context.Context, text, _ string) (SegResult, error) {
		if marker != "" && strings.Contains(text, marker) {
			return SegResult{}, nil
		}
		return SegResult{Fragments: []Segment{{Text: text}}}, nil
	}
}

// The alarm must fire when a page's content does not come back.
func TestSegmentShortDetectsADroppedPage(t *testing.T) {
	page1 := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 12)
	page2 := "EXISTING CORNERS A = FOUND 1/2 REBAR SHMT 0.2 N AND 0.2 W OF CALCULATED POSITION. " +
		strings.Repeat("B = FOUND 5/8 REBAR MONIER 16 INCHES BELOW GRADE. ", 8)

	pages := []resolvedPage{{page: 1, text: page1}, {page: 2, text: page2}}

	var got []stagedFrag
	_, err := segmentLLMWith(context.Background(), drops("EXISTING CORNERS"), pages, 4000, nil,
		func(f stagedFrag) { got = append(got, f) })

	var short *ErrSegmentShort
	if !errors.As(err, &short) {
		t.Fatalf("a dropped page did not raise ErrSegmentShort; err=%v, frags=%d", err, len(got))
	}
	if short.Page != 2 {
		t.Errorf("blamed page %d, want 2", short.Page)
	}
	if short.Out != 0 {
		t.Errorf("reported %d chars out, want 0", short.Out)
	}
}

// A model that reflows and tidies must NOT trip it — that is not loss, and an
// alarm that fires on every document is one nobody reads.
func TestSegmentShortToleratesReflow(t *testing.T) {
	page := strings.Repeat("Lot I of survey recorded under recording number 200808180120. ", 10)
	pages := []resolvedPage{{page: 1, text: page}}
	_, err := segmentLLMWith(context.Background(),
		func(_ context.Context, text, _ string) (SegResult, error) {
			// same words, whitespace collapsed and furniture stripped
			return SegResult{Fragments: []Segment{{Text: strings.Join(strings.Fields(text), " ")}}}, nil
		}, pages, 4000, nil, func(stagedFrag) {})
	if err != nil {
		t.Fatalf("reflow tripped the coverage alarm: %v", err)
	}
}

// A page too short to say anything about must not trip it.
func TestSegmentShortIgnoresTinyPages(t *testing.T) {
	pages := []resolvedPage{{page: 1, text: "p. 3"}}
	_, err := segmentLLMWith(context.Background(), drops("p. 3"), pages, 4000, nil, func(stagedFrag) {})
	if err != nil {
		t.Fatalf("a 4-character page tripped the alarm: %v", err)
	}
}

func TestContentCharsIgnoresWhitespace(t *testing.T) {
	if got := contentChars("  a b\n\tc  "); got != 3 {
		t.Errorf("contentChars = %d, want 3", got)
	}
}
