package main

import (
	"flag"
	"testing"

	"github.com/iodesystems/raglit"
)

// The caption model falls back to the segment model, then the vision model.
// The vision model is often an OCR model, and one captioning a live daemon
// copied first lines and failed the kind list.
func TestIdentifierModel_DefaultsToSegmentModel(t *testing.T) {
	home := raglit.Home(t.TempDir())
	for _, c := range []struct{ vision, segment, want string }{
		{"ocr-model", "text-model", "text-model"},
		{"ocr-model", "", "ocr-model"},
	} {
		f := addLLMFlags(flag.NewFlagSet("t", flag.ContinueOnError))
		*f.visionModel, *f.segmentModel = c.vision, c.segment
		id := f.identifier(home)
		if id == nil || id.Model != c.want {
			t.Errorf("vision=%q segment=%q: caption model = %v, want %q", c.vision, c.segment, id, c.want)
		}
	}
}
