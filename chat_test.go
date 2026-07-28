package raglit

import (
	"context"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// streamReply turns a canned completion into the one-shot stream a Chatter now
// returns. Buffered and closed, so a caller that drains it never blocks.
func streamReply(text string) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk, 2)
	if text != "" {
		ch <- llm.StreamChunk{Content: text}
	}
	ch <- llm.StreamChunk{Done: true}
	close(ch)
	return ch
}

// streamCut is a generation the transport CUT for repetition: the text that
// arrived before the cut, then the finding.
func streamCut(text string, period, reps int) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk, 2)
	ch <- llm.StreamChunk{Content: text}
	ch <- llm.StreamChunk{Done: true, StopReason: llm.StopReasonRepetition,
		Repetition: &llm.RepetitionInfo{Where: "content", Period: period, Reps: reps,
			Trailing: (reps - 1) * period, Sample: strings.Repeat("x", period)}}
	close(ch)
	return ch
}

// optsChatter records the ChatOpts of every call and replays canned replies.
type optsChatter struct {
	replies []string
	calls   int
	opts    []*llm.ChatOpts
}

func (c *optsChatter) ChatStream(_ context.Context, _ []llm.Message, _ []llm.ToolDef,
	o *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.opts = append(c.opts, o)
	r := ""
	if c.calls < len(c.replies) {
		r = c.replies[c.calls]
	}
	c.calls++
	return streamReply(r), nil
}

// An unbounded re-emission is how one 4,112-token unit became 162,000 tokens of
// GPU time. Every call must carry a cap, and it must scale with the input.
func TestSegmenterCapsGeneration(t *testing.T) {
	small := "short unit"
	big := strings.Repeat("lorem ipsum dolor sit amet ", 2000) // ~54 KB

	var caps []int
	for _, in := range []string{small, big} {
		c := &optsChatter{replies: []string{`{"continues_previous":false,"fragments":[{"text":"x"}]}`}}
		if _, err := NewSegmenter(c).SegmentText(context.Background(), in, ""); err != nil {
			t.Fatalf("SegmentText: %v", err)
		}
		if len(c.opts) == 0 || c.opts[0] == nil {
			t.Fatal("segmentation ran with no ChatOpts — max_tokens left to the server")
		}
		if c.opts[0].MaxTokens <= 0 {
			t.Fatalf("MaxTokens = %d, want a positive cap", c.opts[0].MaxTokens)
		}
		caps = append(caps, c.opts[0].MaxTokens)
	}
	if caps[1] <= caps[0] {
		t.Errorf("cap did not scale with input: %d for 10 bytes vs %d for 54 KB", caps[0], caps[1])
	}
	// The cap has to be well under the runaway it exists to stop: that unit was
	// ~4,112 tokens in and 162,000 out.
	if want := 4 * (len(big) / 4); caps[1] > want {
		t.Errorf("cap %d exceeds 4x the input estimate (%d) — too loose to bound a loop", caps[1], want)
	}
}

// A cut answer cannot be valid JSON, so the segmenter must re-prompt — and the
// re-prompt has to SAY it repeated. At temperature 0 a retry that adds nothing
// new reproduces the loop exactly.
func TestSegmenterRetriesACutGeneration(t *testing.T) {
	c := &optsChatter{replies: []string{"", `{"continues_previous":false,"fragments":[{"text":"recovered"}]}`}}
	sg := NewSegmenter(&cuttingChatter{inner: c, cutFirst: 1})

	r, err := sg.SegmentText(context.Background(), "some unit text", "")
	if err != nil {
		t.Fatalf("SegmentText: %v", err)
	}
	if len(r.Fragments) != 1 || r.Fragments[0].Text != "recovered" {
		t.Fatalf("fragments = %+v, want the retry's result", r.Fragments)
	}
	if c.calls < 2 {
		t.Fatalf("made %d calls; a cut generation was not retried", c.calls)
	}
}

// cuttingChatter cuts the first N generations for repetition, then delegates.
// It records EVERY call's opts (cut ones included); the inner stub only sees
// the delegated calls, so its canned replies stay in step.
type cuttingChatter struct {
	inner    *optsChatter
	cutFirst int
	n        int
	opts     []*llm.ChatOpts
}

func (c *cuttingChatter) ChatStream(ctx context.Context, m []llm.Message, td []llm.ToolDef,
	o *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.n++
	c.opts = append(c.opts, o)
	if c.n <= c.cutFirst {
		return streamCut(`{"continues_previous":false,"fragments":[{"text":"aaaa`, 40, 12), nil
	}
	return c.inner.ChatStream(ctx, m, td, o)
}

// A cut TRANSCRIPTION is a page with an unknown amount missing. Indexing it
// would mark the document done and nothing would ever revisit it. It only
// refuses after the loop-break retry ALSO fails.
func TestOCRRefusesACutTranscription(t *testing.T) {
	cc := &cuttingChatter{inner: &optsChatter{}, cutFirst: 5}
	o := NewOCR(cc)
	_, err := o.Page(context.Background(), PageImage{Page: 3, Mime: "image/png", Data: []byte("x")})
	if err == nil {
		t.Fatal("a cut transcription was returned as if it were the page's text")
	}
	if !strings.Contains(err.Error(), "NOT indexed") {
		t.Errorf("error does not say the page was skipped: %v", err)
	}
	if cc.n != 2 {
		t.Errorf("made %d calls, want 2 (the attempt plus one loop-break retry)", cc.n)
	}
}

// Retrying a cut generation UNCHANGED is pointless: measured, the same page was
// cut three times in 34 seconds with byte-identical output, because the server
// runs --temp 0. The retry has to move the SAMPLER.
func TestOCRRetriesACutWithDifferentSampling(t *testing.T) {
	cc := &cuttingChatter{inner: &optsChatter{replies: []string{"the page text"}}, cutFirst: 1}
	o := NewOCR(cc)

	got, err := o.Page(context.Background(), PageImage{Page: 1, Mime: "image/png", Data: []byte("x")})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got != "the page text" {
		t.Errorf("text = %q, want the retry's transcription", got)
	}
	if len(cc.opts) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(cc.opts))
	}
	first, retry := cc.opts[0], cc.opts[1]
	if first.Temperature != nil || first.FrequencyPenalty != nil {
		t.Error("the FIRST attempt should use the server's own sampling, not the loop-break knobs")
	}
	if retry.Temperature == nil || *retry.Temperature <= 0 {
		t.Error("the retry did not raise temperature — a greedy decoder reproduces the loop exactly")
	}
	if retry.FrequencyPenalty == nil || *retry.FrequencyPenalty <= 0 {
		t.Error("the retry did not set a repetition penalty")
	}
	// Transcription, not composition: a hot decoder invents survey text that
	// reads exactly like the real thing.
	if *retry.Temperature > 0.5 {
		t.Errorf("retry temperature %v is high enough to invite fabrication", *retry.Temperature)
	}
	if retry.MaxTokens != first.MaxTokens {
		t.Errorf("retry lost the token cap (%d vs %d)", retry.MaxTokens, first.MaxTokens)
	}
}

func TestOCRCapsGeneration(t *testing.T) {
	c := &optsChatter{replies: []string{"the page text"}}
	if _, err := NewOCR(c).Page(context.Background(),
		PageImage{Page: 1, Mime: "image/png", Data: []byte("x")}); err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(c.opts) == 0 || c.opts[0] == nil || c.opts[0].MaxTokens <= 0 {
		t.Fatal("transcription ran with no max_tokens cap")
	}
}
