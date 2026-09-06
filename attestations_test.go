package raglit

import (
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit/attest"
)

// The log is the record and this is its view, so projecting it twice must land
// exactly where projecting it once did. Incremental appends are what drift; this
// asserts the delete-then-insert contract.
func attestTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPutAttestationsIsIdempotent(t *testing.T) {
	s := attestTestStore(t)
	log := []attest.Entry{
		{Kind: attest.Confirmed, Unit: "p1-aaa", By: "carl", At: "2026-07-31T00:00:00Z"},
		{Kind: attest.Corrected, Unit: "p1-bbb", Text: "the right words", Note: "OCR dropped a digit", By: "carl"},
		{Kind: attest.Affirmed, Blanket: true, Statement: "went through it, minor errors only", By: "carl"},
	}
	for range 3 {
		if err := s.PutAttestations("survey.pdf", log); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Attestations("survey.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows after three projections, want 3", len(got))
	}
	if got[1].Text != "the right words" || got[1].Note != "OCR dropped a digit" {
		t.Errorf("correction did not round-trip: %+v", got[1])
	}
	if !got[2].Blanket || got[2].Statement != "went through it, minor errors only" {
		t.Errorf("blanket affirmation did not round-trip: %+v", got[2])
	}
	if got[0].Seq != 1 || got[2].Seq != 3 {
		t.Errorf("log order not preserved: %d %d", got[0].Seq, got[2].Seq)
	}
}

// A shorter log must not leave the tail of a longer one behind — that is the
// failure delete-then-insert exists to prevent.
func TestPutAttestationsShrinks(t *testing.T) {
	s := attestTestStore(t)
	long := []attest.Entry{
		{Kind: attest.Confirmed, Unit: "a", By: "x"},
		{Kind: attest.Confirmed, Unit: "b", By: "x"},
		{Kind: attest.Confirmed, Unit: "c", By: "x"},
	}
	if err := s.PutAttestations("d.md", long); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAttestations("d.md", long[:1]); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Attestations("d.md")
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 — the tail survived", len(got))
	}
}

// Orphans are kept and are not rebound. A verdict on a unit the current reading
// no longer contains must still be there, because it is what happened.
func TestOrphanedVerdictsSurvive(t *testing.T) {
	s := attestTestStore(t)
	if err := s.PutAttestations("d.md", []attest.Entry{
		{Kind: attest.Confirmed, Unit: "b0-oldunit", By: "carl"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.AttestationsForUnit("d.md", "b0-oldunit")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the verdict on a vanished unit was lost: %d rows", len(rows))
	}
}

// Every ruling on a unit, not the one in force. Which governs is Resolve's call.
func TestAttestationsForUnitKeepsHistory(t *testing.T) {
	s := attestTestStore(t)
	if err := s.PutAttestations("d.md", []attest.Entry{
		{Kind: attest.Confirmed, Unit: "u", By: "carl"},
		{Kind: attest.Retract, Unit: "u", By: "carl"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.AttestationsForUnit("d.md", "u")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want both the confirmation and its retraction", len(rows))
	}
	if rows[0].Kind != string(attest.Confirmed) || rows[1].Kind != string(attest.Retract) {
		t.Errorf("history out of order: %s then %s", rows[0].Kind, rows[1].Kind)
	}
}

func TestAttestedDocsCounts(t *testing.T) {
	s := attestTestStore(t)
	_ = s.PutAttestations("a.md", []attest.Entry{{Kind: attest.Confirmed, Unit: "1", By: "x"}})
	_ = s.PutAttestations("b.pdf", []attest.Entry{
		{Kind: attest.Confirmed, Unit: "1", By: "x"},
		{Kind: attest.Unclear, Unit: "2", By: "x"},
	})
	got, err := s.AttestedDocs()
	if err != nil {
		t.Fatal(err)
	}
	if got["a.md"] != 1 || got["b.pdf"] != 2 {
		t.Errorf("counts wrong: %#v", got)
	}
}
