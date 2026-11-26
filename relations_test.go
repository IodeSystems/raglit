package raglit

import (
	"path/filepath"
	"strings"
	"testing"
)

func tmpJudgements(t *testing.T) *JudgementStore {
	t.Helper()
	js, err := OpenJudgements(filepath.Join(t.TempDir(), "raglit-audit.jsonl"))
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
	audit := filepath.Join(dir, "raglit-audit.jsonl")
	js, err := OpenJudgements(audit)
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

	back, err := OpenJudgements(audit)
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
		NumericOnlyInProbe: []string{"9404150061"},
		NumericOnlyInMatch: []string{"201503110042"},
	})
	if p.Kind != MarkVersion {
		t.Fatalf("want version, got %q — %s", p.Kind, p.Why)
	}
	if !strings.Contains(p.Why, "9404150061") || !strings.Contains(p.Why, "201503110042") {
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
		NumericOnlyInProbe: []string{"25.00", "9404150061"},
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

// ── directional kinds ──────────────────────────────────────────────────

// sp: corrected-by keeps its direction.
//
// The pair is NOT sorted into a canonical order, the store keeps the sides as
// recorded, and the corrected document is returned — present, not hidden. This
// is the composition test: it records in one direction, reads it back in both,
// and checks that no normalization happened anywhere on the way.
func TestCorrectedByKeepsDirection(t *testing.T) {
	js := tmpJudgements(t)
	// Record as given: 2008 is corrected by 2022. A is the corrected one.
	const earlier, later = "2008-survey.pdf", "2022-survey.pdf"
	if err := js.PutRelation(Mark{
		A: earlier, B: later, Kind: MarkCorrectedBy,
		Note: "shed location wrong", By: "carl",
	}); err != nil {
		t.Fatal(err)
	}

	// Read back in the recorded order: sides must come out exactly as given.
	m, ok, err := js.Relation(earlier, later)
	if err != nil || !ok {
		t.Fatalf("not found in recorded order: %v", err)
	}
	if m.A != earlier || m.B != later {
		t.Fatalf("direction lost: got A=%q B=%q, want A=%q B=%q", m.A, m.B, earlier, later)
	}

	// And in the REVERSED order: still found, and the direction is preserved —
	// not flipped to match the order it was asked in.
	m2, ok, err := js.Relation(later, earlier)
	if err != nil || !ok {
		t.Fatalf("not found in reversed order: %v", err)
	}
	if m2.A != earlier || m2.B != later {
		t.Fatalf("reversed query flipped the direction: got A=%q B=%q", m2.A, m2.B)
	}

	// The full list holds exactly one ruling, with the original sides.
	all, err := js.Relations()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("one directional ruling became %d: %+v", len(all), all)
	}
	if all[0].A != earlier || all[0].B != later || all[0].Kind != MarkCorrectedBy {
		t.Errorf("ruling changed in the list: %+v", all[0])
	}

	// The corrected document is NOT hidden: it answers for itself.
	rels, err := js.RelationsFor(earlier)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Fatalf("the corrected document is missing from its own relations: %+v", rels)
	}
	dir, err := js.DirectionalRelationsFor(earlier)
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) != 1 || !dir[0].DocIsA {
		t.Errorf("corrected document must show as the corrected side (A): %+v", dir)
	}
}

// sp: corrected-by may point at a part with the existing grain vocabulary.
//
// The locator uses the same grain the corrections already use — a region id, a
// page number, or an attest unit id — and it is persisted and returned. A whole
// document has an empty locator.
func TestCorrectedByPartGrain(t *testing.T) {
	js := tmpJudgements(t)
	const earlier, later = "a.pdf", "b.pdf"

	// Page grain, the way page_readings locates a correction.
	if err := js.PutRelation(Mark{
		A: earlier, B: later, Kind: MarkCorrectedBy, Locator: Locator{Page: 2},
	}); err != nil {
		t.Fatal(err)
	}
	m, _, _ := js.Relation(earlier, later)
	if m.Locator.Page != 2 || m.Locator.Region != "" || m.Locator.Unit != "" {
		t.Errorf("page grain not kept as one part: %+v", m.Locator)
	}

	// Re-ruling the pair at a different grain replaces the locator.
	if err := js.PutRelation(Mark{
		A: earlier, B: later, Kind: MarkCorrectedBy, Locator: Locator{Region: "p1-rc2"},
	}); err != nil {
		t.Fatal(err)
	}
	m, _, _ = js.Relation(earlier, later)
	if m.Locator.Region != "p1-rc2" || m.Locator.Page != 0 {
		t.Errorf("re-rule did not replace the locator: %+v", m.Locator)
	}

	// Unit grain.
	if err := js.PutRelation(Mark{
		A: earlier, B: later, Kind: MarkCorrectedBy, Locator: Locator{Unit: "p1-bbb"},
	}); err != nil {
		t.Fatal(err)
	}
	if m, _, _ = js.Relation(earlier, later); m.Locator.Unit != "p1-bbb" {
		t.Errorf("unit grain not kept: %+v", m.Locator)
	}

	// A whole-document ruling has no locator, and two parts at once is refused.
	if err := js.PutRelation(Mark{A: earlier, B: later, Kind: MarkCorrectedBy}); err != nil {
		t.Fatalf("a whole-document corrected-by is legal: %v", err)
	}
	if m, _, _ = js.Relation(earlier, later); m.Locator.Count() != 0 {
		t.Errorf("whole-document ruling must have an empty locator: %+v", m.Locator)
	}
	if err := js.PutRelation(Mark{
		A: earlier, B: later, Kind: MarkCorrectedBy,
		Locator: Locator{Page: 2, Region: "p1-rc2"},
	}); err == nil {
		t.Error("accepted a locator naming two parts at once")
	}
}

// sp: correction chains keep every link.
//
// Three documents, each correcting the last. The store must answer with BOTH
// links, each in its own direction, and not collapse the chain into a single
// winner over the first document.
func TestCorrectionChainKeepsLinks(t *testing.T) {
	js := tmpJudgements(t)
	const d1, d2, d3 = "2008-survey.pdf", "2022-03-survey.pdf", "2022-05-survey.pdf"
	// d2 corrects d1 (the shed location); d3 corrects d2 (the metes and bounds).
	if err := js.PutRelation(Mark{A: d1, B: d2, Kind: MarkCorrectedBy, Note: "shed location"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutRelation(Mark{A: d2, B: d3, Kind: MarkCorrectedBy, Note: "metes and bounds"}); err != nil {
		t.Fatal(err)
	}

	// All three documents still exist as rulings' subjects — none hidden.
	for _, doc := range []string{d1, d2, d3} {
		rels, err := js.RelationsFor(doc)
		if err != nil {
			t.Fatal(err)
		}
		if len(rels) == 0 {
			t.Errorf("%s is missing from the relations — corrected documents are not hidden", doc)
		}
	}

	// The middle document is the composition case: it corrects d1 AND is
	// corrected by d3. Both links must come back, each with its own direction.
	corrects, correctedBy, err := js.CorrectionChain(d2)
	if err != nil {
		t.Fatal(err)
	}
	if len(corrects) != 1 || len(correctedBy) != 1 {
		t.Fatalf("chain flattened: d2 corrects %d and is corrected by %d, want one of each", len(corrects), len(correctedBy))
	}
	// outward link: d1 corrected by d2 → A=d1, B=d2; d2 is the correcting (B) side.
	if corrects[0].DocIsA || corrects[0].A != d1 || corrects[0].B != d2 {
		t.Errorf("outward link wrong direction: %+v", corrects[0])
	}
	// inward link: d2 corrected by d3 → A=d2, B=d3; d2 is the corrected (A) side.
	if !correctedBy[0].DocIsA || correctedBy[0].A != d2 || correctedBy[0].B != d3 {
		t.Errorf("inward link wrong direction: %+v", correctedBy[0])
	}

	// From the END of the chain, the whole chain is reachable — two links out,
	// not one collapsed winner.
	allOut, allBy, err := js.CorrectionChain(d3)
	if err != nil {
		t.Fatal(err)
	}
	if len(allOut) != 2 || len(allBy) != 0 {
		t.Fatalf("end of chain: got %d outward / %d inward links, want 2/0", len(allOut), len(allBy))
	}
	// Each link keeps ITS direction: d2 corrected by d3, then d1 corrected by d2.
	if allOut[0].A != d2 || allOut[0].B != d3 || allOut[1].A != d1 || allOut[1].B != d2 {
		t.Errorf("links not in chain order with their own direction: %+v", allOut)
	}

	// From the START, the chain reaches forward: two links in, nothing out.
	startOut, startBy, err := js.CorrectionChain(d1)
	if err != nil {
		t.Fatal(err)
	}
	if len(startOut) != 0 || len(startBy) != 2 {
		t.Fatalf("start of chain: got %d outward / %d inward links, want 0/2", len(startOut), len(startBy))
	}
	// Inward from d1: d1 corrected by d2, then d2 corrected by d3.
	if startBy[0].A != d1 || startBy[0].B != d2 || startBy[1].A != d2 || startBy[1].B != d3 {
		t.Errorf("forward links not in chain order: %+v", startBy)
	}
}
