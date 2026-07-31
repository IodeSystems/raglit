package raglit

import (
	"path/filepath"
	"strings"
	"testing"
)

func tmpJudgements(t *testing.T) *JudgementStore {
	t.Helper()
	js, err := OpenJudgements(filepath.Join(t.TempDir(), "judgements.db"), filepath.Join(t.TempDir(), "raglit-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { js.Close() })
	return js
}

// A pair is ONE fact. Ruled from either direction it is the same ruling, or the
// corpus ends up holding "A is a copy of B" and "B is unrelated to A" at once.
func TestAPairIsOneFactWhicheverWayItIsGiven(t *testing.T) {
	js := tmpJudgements(t)
	if err := js.PutRelation(Mark{A: "z.pdf", B: "a.pdf", Kind: MarkCopy}); err != nil {
		t.Fatal(err)
	}
	for _, order := range [][2]string{{"z.pdf", "a.pdf"}, {"a.pdf", "z.pdf"}} {
		m, ok, err := js.Relation(order[0], order[1])
		if err != nil || !ok {
			t.Fatalf("%v: not found (%v)", order, err)
		}
		if m.Kind != MarkCopy {
			t.Errorf("%v: got %q", order, m.Kind)
		}
	}
	all, err := js.Relations()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("one ruling became %d — the UNIQUE(a,b) constraint is what prevents that", len(all))
	}
	if all[0].A != "a.pdf" {
		t.Errorf("sides not normalized: %+v", all[0])
	}
}

// Rulings must outlive the process that made them, and a correction is a new
// line rather than an edit — the earlier belief stays readable.
// A correction must replace the live answer AND leave the earlier belief
// readable. The append-only file gave the second property for free; a table has
// to keep it on purpose, which is what judgement_log is for.
func TestACorrectionReplacesTheAnswerAndKeepsTheHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "judgements.db")
	audit := filepath.Join(dir, "raglit-audit.jsonl")
	js, err := OpenJudgements(path, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err := js.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: MarkCopy, Note: "first look"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: MarkVersion, Supersedes: "b.pdf", Note: "AF# differs"}); err != nil {
		t.Fatal(err)
	}
	js.Close()

	back, err := OpenJudgements(path, audit)
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	m, ok, err := back.Relation("a.pdf", "b.pdf")
	if err != nil || !ok {
		t.Fatalf("ruling did not survive a reopen (%v)", err)
	}
	if m.Kind != MarkVersion || m.Supersedes != "b.pdf" {
		t.Errorf("the correction did not win: %+v", m)
	}
	hist, err := back.History("relation", "a.pdf\x00b.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("want both rulings in the history, got %d", len(hist))
	}
	if hist[0].Relation == nil || !strings.Contains(hist[0].Relation.Note, "first look") {
		t.Error("the superseded ruling was erased instead of kept")
	}
}

func TestAFreshDatabaseIsAnUnruledCorpusNotAnError(t *testing.T) {
	js := tmpJudgements(t)
	all, err := js.Relations()
	if err != nil {
		t.Fatalf("a corpus nobody has ruled on is not an error: %v", err)
	}
	if len(all) != 0 {
		t.Error("invented rulings from nothing")
	}
}

// Supersedes names a SIDE. A value that is not one of the pair is a typo that
// would otherwise be stored and read later as an ordering.
func TestSupersedesIsValidated(t *testing.T) {
	r := tmpJudgements(t)
	if err := r.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: MarkVersion, Supersedes: "c.pdf"}); err == nil {
		t.Error("accepted a superseding side that is not in the pair")
	}
	if err := r.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: MarkCopy, Supersedes: "a.pdf"}); err == nil {
		t.Error("accepted an ordering on a copy, which has no ordering")
	}
	if err := r.PutRelation(Mark{A: "a.pdf", B: "a.pdf", Kind: MarkCopy}); err == nil {
		t.Error("accepted a document as a copy of itself")
	}
	if err := r.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: "probably"}); err == nil {
		t.Error("accepted an unknown kind")
	}
}

// ── the proposal rule ──────────────────────────────────────────────────

// The case the feature exists for: a re-recorded instrument. High coverage AND a
// number that was substituted, not dropped.
func TestProposesVersionOnPairedNumericSubstitution(t *testing.T) {
	p := Propose(DocMatch{
		Relation:           RelDuplicate,
		Jaccard:            0.95,
		NumericOnlyInProbe: []string{"9308270057"},
		NumericOnlyInMatch: []string{"200806030039"},
	})
	if p.Kind != MarkVersion {
		t.Fatalf("want version, got %q — %s", p.Kind, p.Why)
	}
	if !strings.Contains(p.Why, "9308270057") || !strings.Contains(p.Why, "200806030039") {
		t.Errorf("the why must name what differs: %s", p.Why)
	}
}

// A date substitution is the clearest possible refiling signal and says so.
func TestProposesVersionAndSaysDateWhenADateMoved(t *testing.T) {
	p := Propose(DocMatch{
		Relation:           RelDuplicate,
		NumericOnlyInProbe: []string{"1993"},
		NumericOnlyInMatch: []string{"2008"},
	})
	if p.Kind != MarkVersion {
		t.Fatalf("want version, got %q", p.Kind)
	}
	if !strings.Contains(p.Why, "date") {
		t.Errorf("a moved date should be named as such: %s", p.Why)
	}
}

// The garbled-OCR case — the 2008 lot certification reading "LAURENCE MOONION".
// Nothing was refiled; a scan was read badly. One-sided numeric loss must NOT
// become a version, but it must not be silently confident either.
func TestOneSidedNumericLossIsACopyButNotConfident(t *testing.T) {
	p := Propose(DocMatch{
		Relation:           RelDuplicate,
		Jaccard:            0.95,
		NumericOnlyInProbe: []string{"25.00", "9308270057"},
	})
	if p.Kind != MarkCopy {
		t.Fatalf("want copy, got %q — %s", p.Kind, p.Why)
	}
	if p.Confident {
		t.Error("a one-sided numeric difference is exactly where a real deletion hides — must not be confident")
	}
	if !strings.Contains(p.Why, "25.00") {
		t.Errorf("the missing numbers must be named so they can be checked: %s", p.Why)
	}
}

func TestIdenticalIsAConfidentCopy(t *testing.T) {
	p := Propose(DocMatch{Relation: RelIdentical})
	if p.Kind != MarkCopy || !p.Confident {
		t.Errorf("identical text is a copy and needs no reading: %+v", p)
	}
}

// Containment is still the same instrument — a deed inside a title commitment —
// so it is proposable, and the direction is stated because it decides which
// document is the better evidence.
func TestContainmentIsProposableAndNamesTheDirection(t *testing.T) {
	p := Propose(DocMatch{Relation: RelProbeInside})
	if p.Kind != MarkCopy {
		t.Fatalf("want copy, got %q", p.Kind)
	}
	if !strings.Contains(p.Why, "inside") {
		t.Errorf("direction must be stated: %s", p.Why)
	}
}

// A bare overlap means shared county forms or a quotation. Proposing anything
// there is a claim about meaning the numbers do not support, so it stays open.
func TestOverlapStaysOpenRatherThanGuessing(t *testing.T) {
	p := Propose(DocMatch{Relation: RelOverlap, MatchedChars: 900})
	if p.Kind != "" {
		t.Errorf("overlap should not be classified, got %q", p.Kind)
	}
	if p.Why == "" {
		t.Error("an open pair still owes the reader a reason")
	}
}
