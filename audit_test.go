package raglit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func openJS(t *testing.T, dir string) *JudgementStore {
	t.Helper()
	js, err := OpenJudgements(filepath.Join(dir, "judgements.db"), AuditPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func snapshot(t *testing.T, js *JudgementStore) ([]Mark, []Slice) {
	t.Helper()
	rels, err := js.Relations()
	if err != nil {
		t.Fatal(err)
	}
	sls, err := js.Slices()
	if err != nil {
		t.Fatal(err)
	}
	return rels, sls
}

// THE test. If deleting the database and replaying the trail does not reproduce
// it, then something reached the database that the log never recorded, and the
// audit trail is decoration rather than the source.
//
// Deliberately includes a correction and a deletion: those are the events that
// distinguish replaying a log from concatenating one, because the final state
// depends on ORDER rather than on the set of events.
func TestTheAuditTrailRebuildsTheDatabase(t *testing.T) {
	dir := t.TempDir()
	js := openJS(t, dir)

	if err := js.PutRelation(Mark{A: "b.pdf", B: "a.pdf", Kind: MarkCopy, Note: "first look", By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutRelation(Mark{A: "c.pdf", B: "d.pdf", Kind: MarkUnrelated, By: "carl"}); err != nil {
		t.Fatal(err)
	}
	// A correction: the same pair, ruled differently. Replay must land on this
	// one, not on the first.
	if err := js.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: MarkVersion, Supersedes: "b.pdf", Note: "AF# differs", By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutSlice(Slice{ID: "survey-2008", Parent: "scan.pdf", From: 3, To: 6, Title: "record of survey", By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutSlice(Slice{ID: "doomed", Parent: "scan.pdf", From: 9, To: 10, By: "carl"}); err != nil {
		t.Fatal(err)
	}
	// A deletion: replay must apply the creation and then remove it.
	if err := js.DeleteSlice("doomed"); err != nil {
		t.Fatal(err)
	}
	wantRels, wantSlices := snapshot(t, js)
	js.Close()

	// Reopening IS the rebuild: every answer comes from the trail at runtime, so
	// there is no projection to reconstruct.
	rebuilt := openJS(t, dir)
	defer rebuilt.Close()
	if ev, err := ReadAudit(AuditPath(dir)); err != nil || len(ev) != 6 {
		t.Fatalf("want 6 events in the trail, got %d (%v)", len(ev), err)
	}
	gotRels, gotSlices := snapshot(t, rebuilt)

	if !reflect.DeepEqual(wantRels, gotRels) {
		t.Errorf("relations did not survive a rebuild:\n want %+v\n got  %+v", wantRels, gotRels)
	}
	if !reflect.DeepEqual(wantSlices, gotSlices) {
		t.Errorf("slices did not survive a rebuild:\n want %+v\n got  %+v", wantSlices, gotSlices)
	}

	// The correction won, and the deletion stuck.
	m, ok, _ := rebuilt.Relation("a.pdf", "b.pdf")
	if !ok || m.Kind != MarkVersion || m.Supersedes != "b.pdf" {
		t.Errorf("replay landed on the wrong ruling: %+v", m)
	}
	if _, ok, _ := rebuilt.Slice("doomed"); ok {
		t.Error("a deleted slice came back from the trail")
	}
}

// A database behind its trail catches up on open — the log-first write order
// makes a crash between append and apply normal, so this is the ordinary path.
func TestOpeningCatchesUpOnEventsTheDatabaseMissed(t *testing.T) {
	dir := t.TempDir()
	js := openJS(t, dir)
	if err := js.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: MarkCopy}); err != nil {
		t.Fatal(err)
	}
	js.Close()

	// Simulate the crash window: an event on disk that the database never saw.
	if err := AppendAudit(AuditPath(dir), AuditEvent{
		Op: OpRelationPut, By: "carl",
		Relation: &Mark{A: "c.pdf", B: "d.pdf", Kind: MarkVersion},
	}); err != nil {
		t.Fatal(err)
	}

	back := openJS(t, dir)
	defer back.Close()
	if _, ok, _ := back.Relation("c.pdf", "d.pdf"); !ok {
		t.Error("an event only in the trail was not applied on open")
	}
}

// A line the parser cannot read must stop the rebuild. Skipping it would produce
// a database that quietly disagrees with the record.
func TestAMalformedTrailIsAnErrorNotASkip(t *testing.T) {
	dir := t.TempDir()
	path := AuditPath(dir)
	if err := AppendAudit(path, AuditEvent{Op: OpRelationPut, Relation: &Mark{A: "a", B: "b", Kind: MarkCopy}}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{this is not json}\n")
	f.Close()

	if _, err := ReadAudit(path); err == nil {
		t.Error("a malformed line was skipped instead of reported")
	}
}

// An op from a newer raglit must not be silently dropped.
func TestAnUnknownOpIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := AppendAudit(AuditPath(dir), AuditEvent{Op: "slice.annotate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJudgements(filepath.Join(dir, "judgements.db"), AuditPath(dir)); err == nil {
		t.Error("an unknown op was applied as a no-op instead of refused")
	}
}
