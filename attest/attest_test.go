package attest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }

func turn(start, end float64, label, text string) Unit {
	return Unit{
		Locator:  Locator{Time: &TimeSpan{Start: start, End: end}},
		Label:    label,
		Text:     text,
		Evidence: "pcm-" + label,
	}
}

func region(page int, x, y, w, h float64, text string) Unit {
	return Unit{
		Locator:  Locator{Area: &Area{Page: page, BBox: Rect{X: x, Y: y, W: w, H: h}, DPI: 300}},
		Text:     text,
		Evidence: "crop-" + text,
	}
}

func sealed(t *testing.T, us ...Unit) *Reading {
	t.Helper()
	r := &Reading{Asset: Asset{ID: "a", Kind: KindAudio}, Units: us}
	if err := r.Seal(); err != nil {
		t.Fatalf("seal: %v", err)
	}
	return r
}

// The id must survive everything that is not the claim, and change for
// everything that is. This is the property the whole design rests on: get it
// wrong in the permissive direction and a verdict silently attaches to a claim
// nobody ruled on.
func TestUnitIDIsTheClaim(t *testing.T) {
	base := turn(1, 2, "SPK_0", "the meeting is open")

	same := base
	same.Extra = json.RawMessage(`{"tokens_per_sq_in":4}`)
	if UnitID(same) != UnitID(base) {
		t.Error("producer-specific Extra changed the id; adding a diagnostic field would orphan a corpus")
	}

	for name, mut := range map[string]func(u *Unit){
		"text":     func(u *Unit) { u.Text = "the meeting is closed" },
		"label":    func(u *Unit) { u.Label = "SPK_1" },
		"evidence": func(u *Unit) { u.Evidence = "pcm-other" },
		"start":    func(u *Unit) { u.Locator.Time.Start = 1.5 },
		"end":      func(u *Unit) { u.Locator.Time.End = 2.5 },
		"parent":   func(u *Unit) { u.Parent = "somewhere-else" },
	} {
		got := base
		got.Locator = Locator{Time: &TimeSpan{Start: base.Locator.Time.Start, End: base.Locator.Time.End}}
		mut(&got)
		if UnitID(got) == UnitID(base) {
			t.Errorf("changing %s did not change the id — a different claim would inherit a verdict", name)
		}
	}
}

// Length-prefixed digest fields: two claims must not collide by shifting a
// character across a field boundary.
func TestUnitIDFieldsCannotBleed(t *testing.T) {
	a := turn(1, 2, "A", "BC")
	b := turn(1, 2, "AB", "C")
	a.Evidence, b.Evidence = "e", "e"
	if UnitID(a) == UnitID(b) {
		t.Fatal("label/text boundary is not enforced in the digest")
	}
}

func TestSealRewritesParentHandles(t *testing.T) {
	sheet := region(1, 0, 0, 1, 1, "survey of lot c")
	sheet.ID = "p1" // the producer's own handle
	inner := region(1, 0.1, 0.1, 0.2, 0.2, "REPLAT OF BLOCK 4")
	inner.Parent = "p1"

	r := &Reading{Asset: Asset{ID: "survey.pdf", Kind: KindPDF}, Units: []Unit{sheet, inner}}
	if err := r.Seal(); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if r.Units[1].Parent != r.Units[0].ID {
		t.Fatalf("parent handle not rewritten: %q want %q", r.Units[1].Parent, r.Units[0].ID)
	}
	if kids := r.Children(r.Units[0].ID); len(kids) != 1 {
		t.Fatalf("children: got %d want 1", len(kids))
	}
}

func TestSealRejectsForwardParent(t *testing.T) {
	child := region(1, 0.1, 0.1, 0.2, 0.2, "inner")
	child.Parent = "p1"
	parent := region(1, 0, 0, 1, 1, "sheet")
	parent.ID = "p1"
	r := &Reading{Units: []Unit{child, parent}}
	if err := r.Seal(); err == nil {
		t.Fatal("a child emitted before its parent must be an error, not a dangling id")
	}
}

func TestResolveRulings(t *testing.T) {
	r := sealed(t,
		turn(0, 1, "SPK_0", "one"),
		turn(1, 2, "SPK_1", "two"),
		turn(2, 3, "SPK_1", "three"),
	)
	ids := []string{r.Units[0].ID, r.Units[1].ID, r.Units[2].ID}

	st, err := Resolve(r, []Entry{
		{Kind: Confirmed, Unit: ids[0], By: "carl"},
		{Kind: Corrected, Unit: ids[1], Text: "two, actually", Label: "SPK_0", By: "carl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Stats.Confirmed != 1 || st.Stats.Corrected != 1 || st.Stats.Untouched != 1 {
		t.Fatalf("stats = %+v", st.Stats)
	}
	if st.Units[1].Text != "two, actually" || st.Units[1].Label != "SPK_0" {
		t.Fatalf("correction not applied: %+v", st.Units[1])
	}
	// The machine's claim survives the correction, or there is nothing to score.
	if st.Units[1].Unit.Text != "two" {
		t.Fatalf("correction overwrote the machine's claim: %q", st.Units[1].Unit.Text)
	}
	if !st.Units[1].Corrected() {
		t.Error("a retyped unit must count as ground truth for WER")
	}
	if st.Stats.Complete() {
		t.Error("one untouched unit means the pass is not complete")
	}
}

// A blanket affirmation is a statement about what had been read WHEN IT WAS
// MADE. It must not reach backwards over an individual ruling, and it must not
// reach forwards over a unit that did not exist yet.
func TestBlanketAffirmIsPositional(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"), turn(1, 2, "B", "two"))
	ids := []string{r.Units[0].ID, r.Units[1].ID}

	st, err := Resolve(r, []Entry{
		{Kind: Unclear, Unit: ids[0], By: "carl", Note: "crosstalk"},
		{Kind: Affirmed, Blanket: true, By: "carl", At: "2026-07-29T10:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Units[0].Kind != Unclear {
		t.Errorf("the sweep overwrote an individual ruling: %v", st.Units[0].Kind)
	}
	if st.Units[1].Kind != Affirmed || !st.Units[1].Swept {
		t.Errorf("the sweep did not cover the untouched unit: %+v", st.Units[1])
	}
	if st.Stats.SweptBy != "carl" {
		t.Errorf("sweep provenance lost: %+v", st.Stats)
	}
	if !st.Stats.Complete() {
		t.Error("every unit has a ruling; the pass is complete")
	}
	// The distinction the whole field exists for.
	if st.Stats.Confirmed != 0 {
		t.Error("a sweep must never report as individually confirmed")
	}
}

func TestRetractReturnsToUntouched(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"))
	id := r.Units[0].ID
	st, err := Resolve(r, []Entry{
		{Kind: Corrected, Unit: id, Text: "ONE", By: "carl"},
		{Kind: Retract, Unit: id, By: "carl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Units[0].Kind != "" || st.Stats.Untouched != 1 {
		t.Fatalf("retract left a ruling: %+v", st.Units[0])
	}
	if st.Units[0].Text != "one" {
		t.Fatalf("retract left withdrawn text in the transcript: %q", st.Units[0].Text)
	}
}

// The common repair in both media: the machine cut in the wrong place. The
// replacements must land WHERE the retired unit was, or the transcript reorders
// itself under the reviewer.
func TestResegmentSplicesInPlace(t *testing.T) {
	r := sealed(t,
		turn(0, 1, "A", "one"),
		turn(1, 3, "B", "two three"), // boundary fell mid-sentence
		turn(3, 4, "C", "four"),
	)
	mid := r.Units[1].ID

	st, err := Resolve(r, []Entry{{
		Kind:       Resegment,
		Supersedes: []string{mid},
		Units:      []Unit{turn(1, 2, "A", "two"), turn(2, 3, "B", "three")},
		By:         "carl",
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(st.Units))
	for i, s := range st.Units {
		got[i] = s.Text
	}
	if strings.Join(got, "|") != "one|two|three|four" {
		t.Fatalf("resegment did not splice in place: %v", got)
	}
	if st.Stats.Authored != 2 {
		t.Errorf("authored count = %d, want 2", st.Stats.Authored)
	}
	// Cutting in the right place is not the same as ruling on who spoke.
	if st.Stats.Untouched != 4 {
		t.Errorf("an authored unit is not automatically ruled on: untouched=%d", st.Stats.Untouched)
	}
	// A verdict on a retired unit is not an orphan — it was a real ruling on a
	// claim that has since been replaced.
	st2, err := Resolve(r, []Entry{
		{Kind: Confirmed, Unit: mid, By: "carl"},
		{Kind: Resegment, Supersedes: []string{mid}, Units: []Unit{turn(1, 2, "A", "two")}, By: "carl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Orphaned) != 0 {
		t.Errorf("a superseded unit's verdict was misreported as orphaned: %+v", st2.Orphaned)
	}
}

// The re-read casualty. A verdict naming a claim this reading does not make is
// reported, never matched onto whatever is nearest.
func TestOrphanedVerdictsAreReported(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"))
	st, err := Resolve(r, []Entry{{Kind: Confirmed, Unit: "t0.00-deadbeefdeadbeef", By: "carl"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Orphaned) != 1 {
		t.Fatalf("orphan not reported: %+v", st.Orphaned)
	}
	if st.Stats.Untouched != 1 {
		t.Error("an orphaned verdict must not count towards coverage")
	}
	if !strings.Contains(st.Provenance(), "do not match this reading") {
		t.Errorf("provenance hides the orphan: %s", st.Provenance())
	}
}

func TestDuplicateUnitsAreAProducerBug(t *testing.T) {
	u := turn(0, 1, "A", "one")
	r := &Reading{Units: []Unit{u, u}}
	if _, err := Resolve(r, nil); err == nil {
		t.Fatal("two identical claims must not silently collapse into a wrong unit count")
	}
}

// Two axes, always. Attribution being complete says nothing about the words.
func TestProvenanceSeparatesAttributionFromWords(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"), turn(1, 2, "B", "two"))
	st, err := Resolve(r, []Entry{
		{Kind: Confirmed, Unit: r.Units[0].ID, By: "carl"},
		{Kind: Confirmed, Unit: r.Units[1].ID, By: "carl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := st.Provenance()
	if !strings.Contains(p, "COMPLETE") {
		t.Errorf("complete pass not reported as complete: %s", p)
	}
	if !strings.Contains(p, "WORDS are unverified") {
		t.Errorf("a fully-attributed, never-retyped transcript must still warn about wording: %s", p)
	}

	partial, err := Resolve(r, []Entry{{Kind: Confirmed, Unit: r.Units[0].ID, By: "carl"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(partial.Provenance(), "INCOMPLETE") {
		t.Errorf("half-reviewed pass reported as complete: %s", partial.Provenance())
	}
}

func TestReproducible(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"))
	if ok, _ := r.Reproducible(); !ok {
		t.Error("evidence was recorded; this reading is reproducible")
	}
	bare := Unit{Locator: Locator{Time: &TimeSpan{Start: 0, End: 1}}, Text: "one"}
	r2 := sealed(t, bare)
	ok, id := r2.Reproducible()
	if ok || id != r2.Units[0].ID {
		t.Errorf("a unit with no evidence must name itself: ok=%v id=%q", ok, id)
	}
}

func TestSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "hearing.wav")

	r := &Reading{
		Asset:    Asset{ID: asset, Kind: KindAudio, SHA256: "abc"},
		Producer: "oidio/parakeet",
		Units:    []Unit{turn(0, 1, "A", "one"), turn(1, 2, "B", "two")},
	}
	if _, err := WriteReading(asset, r); err != nil {
		t.Fatal(err)
	}
	if err := Append(asset,
		Entry{Kind: Confirmed, Unit: r.Units[0].ID, By: "carl"},
		Entry{Kind: Affirmed, Blanket: true, By: "carl", At: "2026-07-29T10:00:00Z"},
	); err != nil {
		t.Fatal(err)
	}

	st, err := Load(asset)
	if err != nil {
		t.Fatal(err)
	}
	if st.Stats.Confirmed != 1 || st.Stats.Affirmed != 1 || !st.Stats.Complete() {
		t.Fatalf("stats after round trip = %+v", st.Stats)
	}
	if st.Producer != "oidio/parakeet" {
		t.Errorf("producer lost: %q", st.Producer)
	}

	// Appending again must not rewrite what is there.
	if err := Append(asset, Entry{Kind: Retract, Unit: r.Units[0].ID, By: "carl"}); err != nil {
		t.Fatal(err)
	}
	log, err := ReadLog(asset)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 {
		t.Fatalf("log lost history: %d entries", len(log))
	}
}

func TestRefusesToReadItsOwnOutput(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "survey.pdf")
	for _, p := range []string{ReadingPath(asset), LogPath(asset)} {
		if _, err := WriteReading(p, &Reading{Units: []Unit{turn(0, 1, "A", "x")}}); err == nil {
			t.Errorf("wrote a reading for attest's own output: %s", p)
		}
	}
}

func TestMalformedLogLineIsLoud(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "a.wav")
	if err := writeFile(LogPath(asset), "{\"kind\":\"confirmed\",\"unit\":\"x\",\"by\":\"carl\"}\nnot json\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLog(asset); err == nil {
		t.Fatal("a line nobody could parse must not be silently dropped — that is how a verdict disappears")
	}
}

func TestUnknownVerdictRejected(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "a.wav")
	if err := writeFile(LogPath(asset), "{\"kind\":\"probably\",\"unit\":\"x\",\"by\":\"carl\"}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLog(asset); err == nil {
		t.Fatal("the verdict vocabulary is closed")
	}
	if err := Append(asset, Entry{Kind: Unclear, By: "carl"}); err == nil {
		t.Fatal("a non-blanket verdict needs a unit")
	}
	if err := Append(asset, Entry{Kind: Confirmed, Blanket: true, By: "carl"}); err == nil {
		t.Fatal("only an affirmation may sweep")
	}
}

func TestByMachine(t *testing.T) {
	if !(Entry{By: "agent-vision"}).ByMachine() {
		t.Error("a model reading the document is a machine reading")
	}
	if (Entry{By: "carl"}).ByMachine() {
		t.Error("a person is not a machine")
	}
}

// A ruling nobody signed is a ruling nobody can be asked about. Refused on the
// way in AND on the way out, so a hand-edited log cannot smuggle one past the
// coverage count.
func TestAuthorIsRequired(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "a.wav")
	if err := Append(asset, Entry{Kind: Confirmed, Unit: "x"}); err == nil {
		t.Fatal("wrote an unsigned verdict")
	}
	if err := writeFile(LogPath(asset), "{\"kind\":\"confirmed\",\"unit\":\"x\"}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLog(asset); err == nil {
		t.Fatal("read an unsigned verdict")
	}
}

// The attorney hands the paralegal the link. Both names survive: authorized as
// one, performed by the other.
func TestDelegationKeepsBothNames(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"))
	st, err := Resolve(r, []Entry{{
		Kind: Confirmed, Unit: r.Units[0].ID,
		By: "R. Alvarez (paralegal)", Auth: "acct:jdoe", At: "2026-07-29T10:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Units[0].By != "R. Alvarez (paralegal)" {
		t.Errorf("the person who did the work was lost: %q", st.Units[0].By)
	}
	if st.Units[0].Auth != "acct:jdoe" {
		t.Errorf("the account it was authorized under was lost: %q", st.Units[0].Auth)
	}
}

func TestSignatureRefusesAnonymousAuthor(t *testing.T) {
	principal, by, err := signature(t.Context(), Guest{}, "carl")
	if err != nil {
		t.Fatal(err)
	}
	if principal != "guest" || by != "carl" {
		t.Fatalf("signature = %q/%q, want guest/carl", principal, by)
	}
	if _, _, err := signature(t.Context(), Guest{}, ""); err == nil {
		t.Fatal("the CLI guest may do anything, but still has to say who it is")
	}
}

func TestReadOnlyPermitsOnlyReading(t *testing.T) {
	id := ReadOnly{Guest{}}
	for _, p := range []Permission{PermAttest, PermResegment} {
		if ok, _ := id.Can(t.Context(), "a.wav", p); ok {
			t.Errorf("a read-only mount permitted %s", p)
		}
	}
	if ok, _ := id.Can(t.Context(), "a.wav", PermRead); !ok {
		t.Error("a read-only mount must still permit reading — that is the public case")
	}
}
