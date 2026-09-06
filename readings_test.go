package raglit

import (
	"testing"
)

// A source read twice is one source, and the two readings find each other.
//
// This is the case the corpus could not express: a hearing recording transcribed
// by oidio, and later a transcript whose speaker attribution a person ruled on.
// Two documents, ~40,000 characters each, indexed separately with nothing to say
// they were the same 44 minutes or which one could be quoted.
func TestReadings_TwoReadingsOfOneSourceFindEachOther(t *testing.T) {
	s := testStore(t)
	const rec = "sha-of-the-recording"

	must(t, s.RecordReading(Reading{
		SourceSHA256: rec, SourcePath: "/corpus/hearing.mp4",
		DocPath: "/corpus/hearing.mp4", Method: MethodASR,
		Level: ReadingMachine, ProducedBy: "oidio",
	}))
	must(t, s.RecordReading(Reading{
		SourceSHA256: rec, SourcePath: "/corpus/hearing.mp4",
		DocPath: "/corpus/hearing.verified.md", Method: MethodASR,
		Level: ReadingAttested, RuledBy: "Carl Taylor",
	}))

	all, err := s.ReadingsOfSource(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want two readings of one recording, got %d", len(all))
	}

	sib, err := s.SiblingReadings("/corpus/hearing.verified.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(sib) != 1 || sib[0].DocPath != "/corpus/hearing.mp4" {
		t.Fatalf("the verified transcript cannot find the raw one: %+v", sib)
	}
	if sib[0].Level != ReadingMachine {
		t.Fatalf("sibling level %q — the raw reading must stay marked raw", sib[0].Level)
	}
}

// A re-read replaces that document's reading rather than accumulating rows: it
// is a new account of the same thing, not a second document.
func TestReadings_ReIngestReplacesTheReading(t *testing.T) {
	s := testStore(t)
	for _, by := range []string{"oidio-v1", "oidio-v2"} {
		must(t, s.RecordReading(Reading{
			SourceSHA256: "sha", DocPath: "/corpus/a.mp4",
			Method: MethodASR, Level: ReadingMachine, ProducedBy: by,
		}))
	}
	all, err := s.ReadingsOfSource("sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("a re-read left %d rows, want 1", len(all))
	}
	if all[0].ProducedBy != "oidio-v2" {
		t.Fatalf("the newer read did not replace the older: %+v", all[0])
	}
}

// A reading whose producer could not name its source is STORED, not refused.
//
// oidio's diarized.json names no media and carries no digest, so every
// transcript derived from one arrives unable to point at its recording. Refusing
// the row would hide that; keeping it makes the gap countable — and it must not
// be grouped with other unattributable readings, which would assert a
// relationship nobody established.
func TestReadings_AnUnattributableReadingIsKeptAndNotGrouped(t *testing.T) {
	s := testStore(t)
	must(t, s.RecordReading(Reading{DocPath: "/corpus/x.verified.md", Method: MethodASR}))
	must(t, s.RecordReading(Reading{DocPath: "/corpus/y.verified.md", Method: MethodASR}))

	r, ok, err := s.ReadingFor("/corpus/x.verified.md")
	if err != nil || !ok {
		t.Fatalf("the reading was not kept: ok=%v err=%v", ok, err)
	}
	if r.Level != ReadingMachine {
		t.Fatalf("level defaulted to %q, want machine", r.Level)
	}
	if got, _ := s.ReadingsOfSource(""); len(got) != 0 {
		t.Fatalf("readings with no source were grouped together: %d", len(got))
	}
	if sib, _ := s.SiblingReadings("/corpus/x.verified.md"); len(sib) != 0 {
		t.Fatalf("an unattributable reading claimed %d siblings", len(sib))
	}
}
