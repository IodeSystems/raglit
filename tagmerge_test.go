package raglit

import (
	"context"
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
