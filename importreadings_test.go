package raglit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRecording puts a recording on disk, indexes it, and records the machine
// reading — the state an audio ingest leaves behind.
func seedRecording(t *testing.T, s *Store, dir, name, sha, text string) string {
	t.Helper()
	doc := filepath.Join(dir, name)
	must(t, os.WriteFile(doc, []byte("not really audio"), 0o644))
	must(t, s.Ingest(context.Background(), Document{Path: doc, Title: name,
		Fragments: []Fragment{{Text: text}}}))
	must(t, s.RecordReading(Reading{SourceSHA256: sha, SourcePath: doc, DocPath: doc,
		Method: MethodASR, Level: ReadingMachine, ProducedBy: "oidio", Text: text}))
	return doc
}

// writeTranscript puts a verified transcript beside a recording. It is NOT
// indexed — that is the point: the importer reads it from disk, so it keeps
// working once the corpus stops indexing these sidecars.
func writeTranscript(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	must(t, os.WriteFile(p, []byte(text), 0o644))
	return p
}

// A verified transcript becomes an ATTESTED reading of the recording it
// transcribes, and search then answers with it instead of both.
func TestImportVerifiedTranscripts_AdoptsAndCollapses(t *testing.T) {
	s := testStore(t)
	const machine = "speaker-1 calling first the matter of delano versus mckinnon " +
		"speaker-2 the respondent has the burden to show a substantial change in circumstances"
	const verified = "Clerk calling first the matter of Delano versus McKinnon " +
		"Larry the respondent has the burden to show a substantial change in circumstances"

	dir := t.TempDir()
	rec := seedRecording(t, s, dir, "hearing.mp4", "sha-recording", machine)
	tp := writeTranscript(t, dir, "hearing.verified.md", verified)

	got, err := s.ImportVerifiedTranscripts(false, "Carl Taylor")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Why != "" {
		t.Fatalf("the transcript was not adopted: %+v", got)
	}
	if got[0].Recording != rec {
		t.Fatalf("matched the wrong recording: %+v", got[0])
	}

	r, ok, _ := s.ReadingFor(tp)
	if !ok || r.Level != ReadingAttested || r.RuledBy != "Carl Taylor" {
		t.Fatalf("the adopted reading is not attested to a person: %+v", r)
	}

	// And search now answers once, with the transcript a person ruled on.
	hits, err := s.Search("substantial change in circumstances", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		paths := make([]string, 0, len(hits))
		for _, h := range hits {
			paths = append(paths, h.Path)
		}
		t.Fatalf("the hearing still answers %d times: %v", len(hits), paths)
	}
	// The RECORDING is still the document you cite — its text is now the one a
	// person ruled on. A transcript that is not indexed cannot answer a search,
	// so adopting it has to move the words, not the identity.
	if hits[0].Path != rec {
		t.Fatalf("the answer is not the recording: %s", hits[0].Path)
	}
	if !strings.Contains(hits[0].Text, "Clerk") {
		t.Error("the surviving hit is not the verified wording")
	}
}

// A transcript that matches nothing well enough is REPORTED and left alone.
//
// A wrong match attaches a transcript to the wrong hearing and then ranks it
// above the right one — worse than the untidiness it is fixing, and the failure
// is a quotation attributed to the wrong day in court.
func TestImportVerifiedTranscripts_RefusesToGuess(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	seedRecording(t, s, dir, "sewer.mp4", "sha-a",
		"speaker-1 the district will not accept the side sewer connection as drawn")
	tp := writeTranscript(t, dir, "unrelated.verified.md",
		"Clerk the motion for reconsideration of the protection order is denied")

	got, err := s.ImportVerifiedTranscripts(false, "carl")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want one report, got %d", len(got))
	}
	if got[0].Why == "" {
		t.Fatalf("an unrelated transcript was adopted: %+v", got[0])
	}
	if got[0].Recording != "" {
		t.Fatalf("a refused match still named a recording: %+v", got[0])
	}
	if _, ok, _ := s.ReadingFor(tp); ok {
		t.Fatal("a refused transcript was recorded as a reading anyway")
	}
}

// A dry run reports and records nothing.
func TestImportVerifiedTranscripts_DryRunChangesNothing(t *testing.T) {
	s := testStore(t)
	const txt = "speaker-1 the respondent has the burden to show a substantial change in circumstances today"
	dir := t.TempDir()
	seedRecording(t, s, dir, "h.mp4", "sha", txt)
	tp := writeTranscript(t, dir, "h.verified.md",
		"Clerk the respondent has the burden to show a substantial change in circumstances today")

	got, err := s.ImportVerifiedTranscripts(true, "carl")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Recording == "" {
		t.Fatalf("the dry run did not report a match: %+v", got)
	}
	if _, ok, _ := s.ReadingFor(tp); ok {
		t.Fatal("a dry run recorded a reading")
	}
}
