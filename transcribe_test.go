package raglit

import (
	"os"
	"strings"
	"testing"
)

// The `## Page N` headings are the contract. A consumer resolving a match to a
// page depends on them being complete and monotonic — dropping a blank page
// would silently shift every page number after it, which is exactly the class of
// error this whole feature exists to remove.
func TestTranscriptionKeepsEveryPageIncludingEmptyOnes(t *testing.T) {
	md := RenderTranscription("/x/deed.pdf", []TranscribedPage{
		{Page: 1, Text: "first page"},
		{Page: 2, Text: "   "}, // scanned blank
		{Page: 3, Text: "third page"},
	})
	for _, want := range []string{"## Page 1", "## Page 2", "## Page 3"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(md, "_(no text on this page)_") {
		t.Error("an empty page must be marked, not omitted")
	}
	if strings.Index(md, "## Page 1") > strings.Index(md, "## Page 2") {
		t.Error("pages must be in order")
	}
}

// Out-of-order input still renders in page order — a caller assembling pages
// concurrently must not be able to corrupt the numbering.
func TestTranscriptionSortsByPage(t *testing.T) {
	md := RenderTranscription("/x/d.pdf", []TranscribedPage{
		{Page: 3, Text: "c"}, {Page: 1, Text: "a"}, {Page: 2, Text: "b"},
	})
	if i, j := strings.Index(md, "## Page 1"), strings.Index(md, "## Page 3"); i > j {
		t.Error("pages rendered out of order")
	}
}

// A scanned survey is mostly figure. A transcription that drops the description
// reports the page as blank.
func TestTranscriptionIncludesFigureDescriptions(t *testing.T) {
	md := RenderTranscription("/x/survey.pdf", []TranscribedPage{{
		Page: 1, Text: "SURVEY",
		Figures: []TranscribedFigure{{Kind: "diagram", Description: "plat showing lots F, G, H"}},
	}})
	if !strings.Contains(md, "plat showing lots F, G, H") {
		t.Error("figure description dropped")
	}
	if !strings.Contains(md, "**diagram:**") {
		t.Error("figure should be labelled by kind")
	}
}

// Deterministic, so a diff between two transcriptions is a real change in what
// was read rather than formatting noise.
func TestTranscriptionIsDeterministic(t *testing.T) {
	pages := []TranscribedPage{{Page: 1, Text: "a"}, {Page: 2, Text: "b"}}
	if RenderTranscription("/x/d.pdf", pages) != RenderTranscription("/x/d.pdf", pages) {
		t.Error("render is not deterministic")
	}
}

func TestTranscriptionPathIsBesideTheDocument(t *testing.T) {
	if got := TranscriptionPath("/a/b/c.pdf"); got != "/a/b/c.pdf.raglit-transcription.md" {
		t.Errorf("got %q", got)
	}
}



// raglit must not index its own output. A transcription is written beside the
// document; if discovery picked it up, ingesting it would write a transcription
// OF the transcription, and the corpus would grow a new file every run.
func TestTranscriptionsAreNeverIndexedOrReTranscribed(t *testing.T) {
	var covered bool
	for _, pat := range builtinIgnore {
		if strings.Contains(pat, transcriptionSuffix) {
			covered = true
		}
	}
	if !covered {
		t.Error("builtinIgnore must exclude raglit's own transcriptions")
	}

	// Discovery is one layer; an explicit `raglit index <file>` bypasses it, so
	// the writer refuses too.
	if _, err := WriteTranscription("/x/deed.pdf"+transcriptionSuffix,
		[]TranscribedPage{{Page: 1, Text: "x"}}); err == nil {
		t.Error("writing a transcription OF a transcription must be refused")
	}
	if !IsTranscription("/x/deed.pdf" + transcriptionSuffix) {
		t.Error("IsTranscription should recognise its own suffix")
	}
	if IsTranscription("/x/deed.pdf") {
		t.Error("a source document is not a transcription")
	}
	// The refusal must not have created anything.
	if _, err := os.Stat("/x/deed.pdf" + transcriptionSuffix + transcriptionSuffix); err == nil {
		t.Error("a doubled transcription was created")
	}
}
