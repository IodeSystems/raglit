package raglit

import (
	"context"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// scriptChatter returns canned replies in sequence, one per Chat call.
type scriptChatter struct {
	replies []string
	calls   int
}

func (c *scriptChatter) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolDef) (string, []llm.ToolCall, error) {
	r := ""
	if c.calls < len(c.replies) {
		r = c.replies[c.calls]
	}
	c.calls++
	return r, nil, nil
}

func TestSegmenter_ParsesValidJSON(t *testing.T) {
	c := &scriptChatter{replies: []string{
		"```json\n{\"continues_previous\":false,\"fragments\":[{\"text\":\"alpha\"},{\"text\":\"bravo\"}]}\n```",
	}}
	r, err := NewSegmenter(c).SegmentText(context.Background(), "some text", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ContinuesPrevious || len(r.Fragments) != 2 || r.Fragments[0].Text != "alpha" {
		t.Fatalf("bad parse: %+v", r)
	}
}

func TestSegmenter_FixLoopRetries(t *testing.T) {
	c := &scriptChatter{replies: []string{
		"not json at all",                                    // attempt 0: invalid
		`{"continues_previous":true,"fragments":[{"text":"x"}]}`, // attempt 1: valid
	}}
	r, err := NewSegmenter(c).SegmentText(context.Background(), "t", "open")
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 {
		t.Fatalf("expected a retry, got %d calls", c.calls)
	}
	if !r.ContinuesPrevious || len(r.Fragments) != 1 {
		t.Fatalf("bad retry result: %+v", r)
	}
}

func TestSegmenter_FallsBackToWholeUnit(t *testing.T) {
	c := &scriptChatter{replies: []string{"garbage", "still garbage", "nope"}}
	sg := NewSegmenter(c)
	sg.MaxRetries = 2 // 3 tries total, all bad
	r, err := sg.SegmentText(context.Background(), "the whole window text", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Fragments) != 1 || r.Fragments[0].Text != "the whole window text" {
		t.Fatalf("fallback should be the whole unit: %+v", r)
	}
}

// The heart of the design: an open fragment is DEFERRED across a unit boundary
// and merged when the next unit continues it — and it is never sinked (embedded)
// until it closes.
func TestAssembler_DefersAndMergesOpenFragment(t *testing.T) {
	type sunk struct {
		page, ord int
		text      string
	}
	var got []sunk
	a := NewAssembler(func(page, ord int, text string) error {
		got = append(got, sunk{page, ord, text})
		return nil
	})

	// Page 1: [A, B]. A closes; B is the open (deferred) fragment.
	if err := a.Feed(1, SegResult{Fragments: []Segment{{Text: "A"}, {Text: "B"}}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].text != "A" {
		t.Fatalf("after page1 only A should be sinked (B deferred): %+v", got)
	}

	// Page 2: continues → first fragment C extends B; then D closes B\n\nC.
	if err := a.Feed(2, SegResult{ContinuesPrevious: true, Fragments: []Segment{{Text: "C"}, {Text: "D"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("want 3 finalized fragments, got %d: %+v", len(got), got)
	}
	// A(p1,o0), then the merged B\n\nC keeping B's start position (p1,o1), then D(p2,o0).
	if got[0] != (sunk{1, 0, "A"}) {
		t.Errorf("frag0 = %+v", got[0])
	}
	if got[1].page != 1 || got[1].ord != 1 || !strings.Contains(got[1].text, "B") || !strings.Contains(got[1].text, "C") {
		t.Errorf("merged fragment wrong (should be B+C at p1/o1): %+v", got[1])
	}
	if got[2] != (sunk{2, 0, "D"}) {
		t.Errorf("frag2 = %+v", got[2])
	}
}

func TestAssembler_NonContinuationClosesOpen(t *testing.T) {
	var texts []string
	a := NewAssembler(func(_, _ int, text string) error {
		texts = append(texts, text)
		return nil
	})
	a.Feed(1, SegResult{Fragments: []Segment{{Text: "P"}}})           // P open
	a.Feed(2, SegResult{ContinuesPrevious: false, Fragments: []Segment{{Text: "Q"}}}) // P closes, Q open
	a.Close()
	if len(texts) != 2 || texts[0] != "P" || texts[1] != "Q" {
		t.Fatalf("non-continuation should keep P and Q separate: %v", texts)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                          `{"a":1}`,
		"```json\n{\"a\":1}\n```":          `{"a":1}`,
		"here you go: {\"a\":1} thanks":    `{"a":1}`,
		"```\n{\"a\":[1,2]}\n```":          `{"a":[1,2]}`,
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}
