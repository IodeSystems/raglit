package raglit

import (
	"context"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// namedChatter records that it was called, so a test can tell WHICH client did
// the segmenting — the whole point of a separate segment model.
type namedChatter struct {
	name  string
	calls *[]string
	reply string
}

func (c *namedChatter) ChatStream(_ context.Context, _ []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	*c.calls = append(*c.calls, c.name)
	return streamReply(c.reply), nil
}

// The defect this exists for: the PDF path built its own segmenter off the OCR
// client and so ignored a configured segment model entirely. Every PDF and image
// in a corpus went through that path, so the setting would have looked inert.
func TestWorkerUsesItsConfiguredSegmenter(t *testing.T) {
	var calls []string
	s := openMem(t)
	ocrC := &namedChatter{name: "vision", calls: &calls, reply: "transcribed page text"}
	segC := &namedChatter{name: "segment", calls: &calls,
		reply: `{"continues_previous":false,"fragments":[{"text":"a fragment"}]}`}
	w := &Worker{Store: s, OCR: NewOCR(ocrC), Segmenter: NewSegmenter(segC)}
	if got := w.segmenter(); got == nil {
		t.Fatal("worker returned no segmenter")
	}
	// It must be the CONFIGURED one, not one derived from the OCR client.
	if _, _, err := s.ingestUnits(context.Background(), w.segmenter(), w.OCR,
		"d.pdf", "D", []ingestUnit{{page: 1, mime: "image/png", data: []byte("img")}},
		FragConfig{}, nil); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	sawSegment := false
	for _, c := range calls {
		if c == "segment" {
			sawSegment = true
		}
	}
	if !sawSegment {
		t.Errorf("the configured segmenter was never called; calls=%v", calls)
	}
}

// A worker that never set the field must behave exactly as it always did, or
// wiring this in changes every existing caller.
func TestWorkerWithoutASegmenterFallsBackToTheOCRClient(t *testing.T) {
	var calls []string
	ocrC := &namedChatter{name: "vision", calls: &calls, reply: "x"}
	w := &Worker{OCR: NewOCR(ocrC)}
	if w.segmenter() == nil {
		t.Fatal("fallback must produce a segmenter, not nil")
	}
	// And with no OCR either, nil rather than a panic.
	if (&Worker{}).segmenter() != nil {
		t.Error("a worker with no OCR and no segmenter should yield nil")
	}
}
