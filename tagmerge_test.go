package raglit

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestParseTagMerge_ReadsTheSpecAndRefusesAHalfOne(t *testing.T) {
	m, err := ParseTagMerge(" LBP , Lead-Based Paint => lead paint ")
	if err != nil {
		t.Fatal(err)
	}
	if m.To != "lead paint" || len(m.From) != 2 || m.From[0] != "lbp" {
		t.Fatalf("merge = %+v", m)
	}
	for _, bad := range []string{"lead paint", "=>lead paint", "lbp=>", "lead paint=>lead paint"} {
		if _, err := ParseTagMerge(bad); err == nil {
			t.Errorf("%q parsed, want an error", bad)
		}
	}
}

func TestMergeTags_CollapsesAcrossTheIndexAndDeduplicates(t *testing.T) {
	s := storeWithDocs(t, 3)
	ctx := context.Background()
	tagged(t, s, "/corpus/scan_000.pdf", "analysis",
		[]string{"lbp", "escrow closing"}, []string{"report"})
	// This one carries BOTH spellings: the merge must leave one tag, not two of
	// the same, or the count it reports is a count of a duplicate.
	tagged(t, s, "/corpus/scan_001.pdf", "analysis",
		[]string{"lead paint", "lbp"}, []string{"report"})
	tagged(t, s, "/corpus/scan_002.pdf", "deed",
		[]string{"boundary survey"}, []string{"reference"})

	res, err := s.MergeTags(ctx, TagMerge{From: []string{"lbp", "never used"}, To: "lead paint"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Documents != 2 {
		t.Errorf("merged %d documents, want 2", res.Documents)
	}
	if len(res.Collapsed) != 1 || res.Collapsed[0] != "lbp" {
		t.Errorf("collapsed = %v", res.Collapsed)
	}
	// A tag nobody carried is reported as such rather than silently succeeding:
	// a merge that matched nothing is usually a typo.
	if len(res.Missing) != 1 || res.Missing[0] != "never used" {
		t.Errorf("missing = %v", res.Missing)
	}

	got, err := s.DocumentIdentity("/corpus/scan_001.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ContentTags) != 1 || got.ContentTags[0] != "lead paint" {
		t.Errorf("tags after merge = %v — want one 'lead paint'", got.ContentTags)
	}
	// The caption is not what was merged.
	if got.Name == "" || got.Kind != "analysis" {
		t.Errorf("the merge touched the caption: %+v", got)
	}
	// And the searchable identity fragment moved with the columns, or search
	// would rank documents by a tag they no longer carry.
	hits, err := s.Search("lead paint", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Origin == fragOriginIdentity && strings.Contains(strings.ToLower(h.Text), "lbp") {
			t.Errorf("the identity fragment still says lbp: %q", h.Text)
		}
	}
	// Untouched documents stay untouched.
	other, err := s.DocumentIdentity("/corpus/scan_002.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(other.ContentTags) != 1 || other.ContentTags[0] != "boundary survey" {
		t.Errorf("an unrelated document changed: %v", other.ContentTags)
	}
}

// The near-duplicate report is a PROPOSAL a person rules on, so it must be
// legible: whole-word matching, not substrings that pair "data" with "metadata".
func TestTagNeighbours_MatchesWholeWordsOnly(t *testing.T) {
	s := storeWithDocs(t, 2)
	tagged(t, s, "/corpus/scan_000.pdf", "deed",
		[]string{"lead paint", "metadata schema"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "deed",
		[]string{"lead paint inspection", "data retention"}, []string{"reference"})

	near, err := s.TagNeighbours()
	if err != nil {
		t.Fatal(err)
	}
	if got := near["lead paint"]; len(got) != 1 || got[0] != "lead paint inspection" {
		t.Errorf("near[lead paint] = %v", got)
	}
	for _, n := range near["data retention"] {
		if n == "metadata schema" {
			t.Error("matched \"data\" inside \"metadata\" — substring matching is back")
		}
	}
}

// Tags are stored comma-separated, so a comma inside one comes back as two tags
// on the next read — silently, and only for whichever documents got one.
func TestValidateTags_ATagCannotCarryTheSeparator(t *testing.T) {
	ct, _, err := validateTags(
		[]string{"Escrow, Closing", "lead paint", "boundary survey"},
		[]string{"reference"})
	if err != nil {
		t.Fatal(err)
	}
	if ct[0] != "escrow closing" {
		t.Fatalf("tags = %v — want the comma gone", ct)
	}
	if got := splitTagList(strings.Join(ct, ",")); len(got) != len(ct) {
		t.Errorf("round trip turned %d tags into %d: %v", len(ct), len(got), got)
	}
}

// A whole list in one string is the model's mistake, not a long tag: split it
// rather than drop it and fail the caption for "1 usable" tag.
func TestValidateTags_AListInOneStringIsSplit(t *testing.T) {
	ct, _, err := validateTags(
		[]string{"boundary dispute", "deed, easement; boundary survey, quiet title"},
		[]string{"reference"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"boundary dispute", "deed", "easement", "boundary survey", "quiet title"}
	if strings.Join(ct, "|") != strings.Join(want, "|") {
		t.Fatalf("tags = %v, want %v", ct, want)
	}
}

// The trail stays additive: a tag.merge entry is appended to a trail that
// already holds every other kind of ruling, and none of those rulings moves.
// The store answers the SAME as before the merge, plus the new answer.
func TestTagMergeEntryStaysAdditive(t *testing.T) {
	dir := t.TempDir()
	js := openJS(t, dir)

	// Seed a trail with one of every other kind of entry.
	if err := js.PutAuthority(ReadingAuthority{Source: "sha-source-1", Doc: "doc.pdf", Why: "attested", By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutRelation(Mark{A: "a.pdf", B: "b.pdf", Kind: MarkCopy, By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutSlice(Slice{ID: "s1", Parent: "scan.pdf", From: 1, To: 3, By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutPageCorrection(PageCorrection{Doc: "doc.pdf", Page: 2, Text: "2015", By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if err := js.Withdraw(Withdrawal{Path: "out.pdf", Reason: "draft, not record", By: "carl"}); err != nil {
		t.Fatal(err)
	}

	before := snapshotAll(t, js)

	// Apply a tag merge: it appends one entry.
	if err := js.RecordTagMerge(TagMergeRuling{
		From: []string{"lbp"}, To: "lead paint",
		Documents: []string{"doc.pdf"}, By: "carl",
	}); err != nil {
		t.Fatal(err)
	}

	// Every pre-existing entry is unchanged: the answers that existed before the
	// merge are the same after it. The merge's own answer and the trail length
	// are the things that are ALLOWED to change, so they are checked on their
	// own, below.
	after := snapshotAll(t, js)
	if !reflect.DeepEqual(before.Relations, after.Relations) {
		t.Errorf("the merge changed a relation: before %+v after %+v", before.Relations, after.Relations)
	}
	if !reflect.DeepEqual(before.Slices, after.Slices) {
		t.Errorf("the merge changed a slice: before %+v after %+v", before.Slices, after.Slices)
	}
	if !reflect.DeepEqual(before.Authority, after.Authority) {
		t.Errorf("the merge changed an authority: before %+v after %+v", before.Authority, after.Authority)
	}
	if !reflect.DeepEqual(before.Corrections, after.Corrections) {
		t.Errorf("the merge changed a correction: before %+v after %+v", before.Corrections, after.Corrections)
	}
	if !reflect.DeepEqual(before.Withdrawals, after.Withdrawals) {
		t.Errorf("the merge changed a withdrawal: before %+v after %+v", before.Withdrawals, after.Withdrawals)
	}

	// The new answer is present, and the trail holds one more event.
	if _, ok := js.TagMerge([]string{"lbp"}, "lead paint"); !ok {
		t.Error("the recorded tag merge is not answered")
	}
	events, err := ReadAudit(AuditPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != before.Events+1 {
		t.Errorf("trail grew from %d to %d events, want +1", before.Events, len(events))
	}
	if events[len(events)-1].Op != OpTagMerge {
		t.Errorf("last event is %q, want %q", events[len(events)-1].Op, OpTagMerge)
	}
}

// A re-derivation finds the recorded ruling: close the trail, reopen it, and
// TagMerges() answers the ruling the log recorded. A changed mind — a second
// ruling for the same normalized pair — answers as the last one, while the
// first stays in the trail.
func TestReopenedTrailFindsTheTagMergeRuling(t *testing.T) {
	dir := t.TempDir()
	js := openJS(t, dir)
	if err := js.RecordTagMerge(TagMergeRuling{
		From: []string{"lbp"}, To: "lead paint",
		Documents: []string{"doc.pdf", "scan.pdf"}, By: "carl",
	}); err != nil {
		t.Fatal(err)
	}
	js.Close()

	// Reopen: the store must re-derive its answer from the trail, not from the
	// in-memory state that is now gone.
	back := openJS(t, dir)
	defer back.Close()

	r, ok := back.TagMerge([]string{"lbp"}, "lead paint")
	if !ok {
		t.Fatal("a re-derivation did not find the recorded ruling")
	}
	if r.To != "lead paint" || !containsTag(r.From, "lbp") {
		t.Errorf("ruling came back wrong: %+v", r)
	}
	if !reflect.DeepEqual(sorted(r.Documents), []string{"doc.pdf", "scan.pdf"}) {
		t.Errorf("documents = %v", r.Documents)
	}

	// A changed mind: a second ruling for the same pair.
	if err := back.RecordTagMerge(TagMergeRuling{
		From: []string{"lbp"}, To: "lead paint",
		Documents: []string{"doc.pdf"}, By: "carl",
	}); err != nil {
		t.Fatal(err)
	}
	// The answer is the LAST one.
	again, _ := back.TagMerge([]string{"lbp"}, "lead paint")
	if !reflect.DeepEqual(sorted(again.Documents), []string{"doc.pdf"}) {
		t.Errorf("after a second ruling the answer is %v, want the last one", again.Documents)
	}
	// TagMerges answers one fact per normalized pair, not two.
	all := back.TagMerges()
	if len(all) != 1 {
		t.Fatalf("TagMerges = %d entries, want 1 (one fact per pair)", len(all))
	}
	// ...but the trail keeps both.
	events, err := ReadAudit(AuditPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	merges := 0
	for _, e := range events {
		if e.Op == OpTagMerge {
			merges++
		}
	}
	if merges != 2 {
		t.Errorf("trail holds %d tag.merge entries, want 2 (the changed mind kept both)", merges)
	}
}

// The store does not re-derive: TagMerges() answers what the trail recorded,
// and the empty case answers empty.
func TestTagMerges_AnswersOnlyWhatIsRecorded(t *testing.T) {
	dir := t.TempDir()
	js := openJS(t, dir)
	if got := js.TagMerges(); len(got) != 0 {
		t.Fatalf("an empty trail answers %d merges, want 0", len(got))
	}
	if _, ok := js.TagMerge([]string{"a"}, "b"); ok {
		t.Error("an unrecorded merge was answered")
	}

	if err := js.RecordTagMerge(TagMergeRuling{From: []string{"z", "a"}, To: "k"}); err != nil {
		t.Fatal(err)
	}
	// The normalized key: "z,a=>k" is the same ruling as "a,z=>k".
	if _, ok := js.TagMerge([]string{"a", "z"}, "k"); !ok {
		t.Error("the ruling was not found under its normalized key")
	}
	all := js.TagMerges()
	if len(all) != 1 {
		t.Fatalf("TagMerges = %d, want 1", len(all))
	}
}

// allAnswers captures every answer the store gives from a trail, so a test can
// show that appending one entry changed nothing else.
type allAnswers struct {
	Relations   []Mark
	Slices      []Slice
	Authority   []ReadingAuthority
	Corrections map[int]PageCorrection
	Withdrawals []Withdrawal
	TagMerges   []TagMergeRuling
	Events      int
}

func snapshotAll(t *testing.T, js *JudgementStore) allAnswers {
	t.Helper()
	rels, err := js.Relations()
	if err != nil {
		t.Fatal(err)
	}
	sls, err := js.Slices()
	if err != nil {
		t.Fatal(err)
	}
	auth := js.Authorities()
	corr, err := js.PageCorrections("doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	events, err := ReadAudit(js.audit)
	if err != nil {
		t.Fatal(err)
	}
	return allAnswers{
		Relations:   rels,
		Slices:      sls,
		Authority:   auth,
		Corrections: corr,
		Withdrawals: js.Withdrawals(),
		TagMerges:   js.TagMerges(),
		Events:      len(events),
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func containsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}
