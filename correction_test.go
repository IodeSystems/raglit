package raglit

import (
	"path/filepath"
	"strings"
	"testing"
)

// The failure this exists for: identifiers read off 200% crops by a person lived
// in the .raglit-transcription.md file, which raglit rewrites on every read.
// They were destroyed twice by ordinary re-reads. A correction has to survive
// re-reading and be RE-ISSUED into every later render.
func TestACorrectionSurvivesAndIsReissued(t *testing.T) {
	dir := t.TempDir()
	js, err := OpenJudgements(filepath.Join(dir, "judgements.db"), AuditPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	doc := "/corpus/survey.pdf"
	if err := js.PutPageCorrection(PageCorrection{
		Doc: doc, Page: 1,
		Text: "AF 200808180120, surveyor KEVIN G. HALVOR",
		Note: "read at 200% off the native 960 ppi image", By: "carl", At: "2026-07-31",
	}); err != nil {
		t.Fatal(err)
	}

	machine := []TranscribedPage{{Page: 1, Text: "AF 2008081020, surveyor HALVR"}}

	// Every render re-issues it — this is the "not losing the stuff" property.
	for i, name := range []string{"first render", "render after a re-read"} {
		got, err := js.PageCorrections(doc)
		if err != nil {
			t.Fatal(err)
		}
		md := RenderTranscriptionCorrected(doc, machine, got)
		if !strings.Contains(md, "KEVIN G. HALVOR") {
			t.Errorf("%s (%d): the correction was not applied:\n%s", name, i, md)
		}
		if strings.Contains(md, "HALVR,") || strings.Contains(md, "2008081020,") {
			t.Errorf("%s: the machine read survived alongside the correction", name)
		}
		// And the reader can tell a checked page from a machine one.
		if !strings.Contains(md, "corrected by hand") || !strings.Contains(md, "carl") {
			t.Errorf("%s: a corrected page must say so and say who:\n%s", name, md)
		}
		if !strings.Contains(md, "200%") {
			t.Errorf("%s: the note on HOW it was established must survive", name)
		}
	}
	js.Close()

	// And it survives losing the database entirely, because it is in the trail.
	back, err := OpenJudgements(filepath.Join(dir, "judgements.db"), AuditPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	if _, err := back.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got, err := back.PageCorrections(doc)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := got[1]; !ok || !strings.Contains(c.Text, "KEVIN G. HALVOR") {
		t.Errorf("the correction did not survive a rebuild from the audit trail: %+v", got)
	}
}

// An uncorrected render must be unchanged — corrections are additive.
func TestNoCorrectionsRendersExactlyAsBefore(t *testing.T) {
	pages := []TranscribedPage{{Page: 1, Text: "some text"}}
	if RenderTranscriptionCorrected("/x.pdf", pages, nil) != RenderTranscription("/x.pdf", pages) {
		t.Error("rendering with no corrections diverged from the plain render")
	}
}

// The generated file has to say it is generated. It collected hand edits twice
// because its header described it as raglit's output without saying edits die.
func TestTheExportSaysEditsAreLost(t *testing.T) {
	md := RenderTranscription("/x.pdf", []TranscribedPage{{Page: 1, Text: "t"}})
	for _, want := range []string{"GENERATED FILE", "lost", "--correct"} {
		if !strings.Contains(md, want) {
			t.Errorf("the export must warn (%q missing):\n%s", want, md[:400])
		}
	}
}
