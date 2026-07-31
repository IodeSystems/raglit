package raglit

import (
	"context"
	"path/filepath"
	"testing"
)

// A correction is an attestation that a better reading exists — not an erasure.
// The old text stays as a row, marked inactive, because it is what the index
// held when the document was cited and what a stale quotation still matches.
func TestReadingsAccumulateAndTheOldOneIsKept(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	js, err := OpenJudgements("", AuditPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	RecordReadingsInto(js, s)

	doc := "/corpus/survey.pdf"
	// A machine read, then a person's correction of it.
	if err := js.PutPageCorrection(PageCorrection{
		Doc: doc, Page: 1, Text: "AF 200808180120 KEVIN G. HALVOR",
		Supersedes: "AF 2008081020 HALVR",
		Note:       "read at 200%", By: "carl", At: "2026-07-31",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.PageReadings(context.Background(), doc, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want two rows — the machine read and the correction — got %d: %+v", len(got), got)
	}
	if got[0].Source != "machine" || got[0].Active {
		t.Errorf("the machine read must be kept and inactive: %+v", got[0])
	}
	if got[0].Text != "AF 2008081020 HALVR" {
		t.Errorf("the superseded text was not preserved: %q", got[0].Text)
	}
	if got[1].Source != "corrected" || !got[1].Active {
		t.Errorf("the correction must be the active reading: %+v", got[1])
	}
	if got[1].Seq <= got[0].Seq {
		t.Errorf("readings must be ordered: %d then %d", got[0].Seq, got[1].Seq)
	}
	if got[1].By != "carl" || got[1].Note != "read at 200%" {
		t.Errorf("who checked it and how must survive: %+v", got[1])
	}

	// A second correction supersedes the first; all three stay.
	if err := js.PutPageCorrection(PageCorrection{
		Doc: doc, Page: 1, Text: "AF 200808180120 KEVIN G. HALVOR, cert 20123169", By: "carl",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PageReadings(context.Background(), doc, 1)
	if len(got) != 3 {
		t.Fatalf("want three rows after a second correction, got %d", len(got))
	}
	active := 0
	for _, r := range got {
		if r.Active {
			active++
		}
	}
	if active != 1 {
		t.Errorf("exactly one reading may be active, got %d", active)
	}
	if !got[2].Active {
		t.Errorf("the newest reading must be the active one: %+v", got[2])
	}
}

// Re-reading a document nobody corrected must not grow a row per read.
func TestAnIdenticalRereadIsNotANewVersion(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.AddPageReading(ctx, PageReading{
			Doc: "/x.pdf", Page: 1, Text: "same text every time", Source: "machine",
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.PageReadings(ctx, "/x.pdf", 1)
	if len(got) != 1 {
		t.Errorf("identical re-reads made %d rows, want 1", len(got))
	}
}
