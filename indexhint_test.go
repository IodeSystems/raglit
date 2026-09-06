package raglit

import (
	"context"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// The hint is the corpus owner's account of how to READ the collection, so the
// place it matters most is the place the text is produced. These hold it to the
// two prompts it reaches outside identity — the ones a fake identity chatter
// cannot see.

// promptSpy records the text parts of every call.
type promptSpy struct {
	prompts []string
	reply   string
}

func (c *promptSpy) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		for _, p := range m.Parts {
			if p.Type == "" || p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
	}
	c.prompts = append(c.prompts, b.String())
	return streamReply(c.reply), nil
}

const readingHint = "RO means repair order, not received. Part numbers are in the right margin."

// Where a fragment boundary falls depends on knowing what the document IS: a
// lab panel's results are one unit and a work order's line items another, and
// nothing on the page says so.
func TestIndexHint_ReachesTheSegmentationPrompt(t *testing.T) {
	c := &promptSpy{reply: `{"continues_previous":false,"fragments":[{"text":"alpha"}]}`}
	sg := NewSegmenter(c)
	sg.Collection = readingHint
	if _, err := sg.SegmentText(context.Background(), "some text", ""); err != nil {
		t.Fatal(err)
	}
	if len(c.prompts) == 0 || !strings.Contains(c.prompts[0], readingHint) {
		t.Fatalf("segmentation prompt carries no hint:\n%s", strings.Join(c.prompts, "\n---\n"))
	}
	// And an index with no hint is not handed an empty preamble about one.
	c2 := &promptSpy{reply: `{"continues_previous":false,"fragments":[{"text":"alpha"}]}`}
	if _, err := NewSegmenter(c2).SegmentText(context.Background(), "some text", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c2.prompts[0], "ABOUT THIS COLLECTION") {
		t.Errorf("an empty hint still rendered a block:\n%s", c2.prompts[0])
	}
}

// "How to decode" is a property of the PIXELS. A model reading one page cannot
// infer that a carbon copy's second column is the customer's, and this is the
// call where that knowledge changes what the text says.
func TestIndexHint_ReachesTheTranscriptionPrompt(t *testing.T) {
	img := PageImage{Page: 1, Mime: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}
	about := RegionAbout{Doc: "/c/ro-4471.pdf", Page: 1, Depth: 0, FitsWhole: true}

	c := &promptSpy{reply: "RO 4471 — brake pads, 2ea."}
	o := NewOCR(c)
	o.Collection = readingHint
	if _, err := o.AskWithOCR()(context.Background(), img, about); err != nil {
		t.Fatal(err)
	}
	if len(c.prompts) == 0 || !strings.Contains(c.prompts[0], readingHint) {
		t.Fatalf("transcription prompt carries no hint:\n%s", strings.Join(c.prompts, "\n---\n"))
	}

	// Every kind of reading turn, not just the plain one: a root ask, a crop
	// ask, and the escalation that decides whether a page is upside down. The
	// hint is context about the collection rather than a question, so it does
	// not compete with the one being asked — and the turn that decides what a
	// page IS benefits from knowing what the corpus is.
	for _, tc := range []struct {
		name  string
		about RegionAbout
	}{
		{"root", RegionAbout{Depth: 0}},
		{"crop", RegionAbout{Depth: 1}},
		{"escalation", RegionAbout{Depth: 1, Escalation: "is this page upside down?"}},
	} {
		spy := &promptSpy{reply: "text"}
		oo := NewOCR(spy)
		oo.Collection = readingHint
		if _, err := oo.AskWithOCR()(context.Background(), img, tc.about); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(spy.prompts) == 0 || !strings.Contains(spy.prompts[0], readingHint) {
			t.Errorf("%s turn carries no hint:\n%s", tc.name, strings.Join(spy.prompts, "\n---\n"))
		}
	}

	// No hint, no preamble.
	bare := &promptSpy{reply: "text"}
	if _, err := NewOCR(bare).AskWithOCR()(context.Background(), img, about); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare.prompts[0], "ABOUT THIS COLLECTION") {
		t.Errorf("an empty hint still rendered a block:\n%s", bare.prompts[0])
	}
}
