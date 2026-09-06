package raglit

import (
	"strings"
	"testing"
)

// modText renders one modifier alone, which is what the tests that used to call
// the suffix functions directly are actually asserting about.
func modText(m Mod) string {
	var b strings.Builder
	m(&b)
	return b.String()
}

// Models wrap the answer in an ARRAY. The greedy regex spanned it and produced
// invalid JSON, so the raw reply became the transcription — on every region of
// every survey read.
func TestParsesAnArrayWrappedReply(t *testing.T) {
	got := ParseRegionReading(`[{"transcription_markdown":"LOT 12","description":"a plat","kind":"drawing","regions":[]}]`)
	if got.Transcription != "LOT 12" {
		t.Errorf("array-wrapped reply not parsed: %q", got.Transcription)
	}
	if got.Description != "a plat" {
		t.Errorf("description not parsed: %q", got.Description)
	}
	// Braces inside a transcription must not end the object early.
	got = ParseRegionReading(`{"transcription_markdown":"NOTE {see sheet 2}","description":"d"}`)
	if got.Transcription != "NOTE {see sheet 2}" {
		t.Errorf("braces inside a string broke the scan: %q", got.Transcription)
	}
}
