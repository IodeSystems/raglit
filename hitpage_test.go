package raglit

import "testing"

// The bug: a fragment spans pages, `fragments.page` records only where it
// started, and search reported that. A quotation from the fourth message of an
// archive was cited as the second.
func TestAMatchIsCitedToThePageItIsActuallyOn(t *testing.T) {
	// One fragment covering three pages, boundaries at 0, 20 and 50.
	spans := `[{"off":0,"page":2},{"off":20,"page":3},{"off":50,"page":4}]`
	text := "page two text here..|page three text here.......|page four text is here"

	for _, c := range []struct {
		query string
		want  int
	}{
		{"two", 2},
		{"three", 3},
		{"four", 4},
	} {
		if got := HitPage(2, spans, text, c.query); got != c.want {
			t.Errorf("query %q: cited page %d, want %d", c.query, got, c.want)
		}
	}
}

// Nothing better knowable → the old answer, not a wrong one dressed up.
func TestFallsBackToTheStartPageWhenNothingIsLocatable(t *testing.T) {
	spans := `[{"off":0,"page":7},{"off":20,"page":8}]`
	if got := HitPage(7, spans, "some text", "absentterm"); got != 7 {
		t.Errorf("no locatable term: got %d, want the start page 7", got)
	}
	// A fragment that does not cross a boundary is already right.
	if got := HitPage(5, `[{"off":0,"page":5}]`, "text", "text"); got != 5 {
		t.Errorf("single-span fragment: got %d, want 5", got)
	}
	if got := HitPage(9, "", "text", "text"); got != 9 {
		t.Errorf("no spans recorded: got %d, want 9", got)
	}
}

// FTS5 operators are not text to look for. Anchoring on the literal "AND" would
// put a quotation on whatever page that stopword happened to fall on.
func TestQuerySyntaxIsNotTreatedAsSearchableText(t *testing.T) {
	terms := queryTerms(`"easement" AND (survey OR plat) NOT vacated`)
	for _, bad := range []string{"and", "or", "not", `"`, "("} {
		for _, got := range terms {
			if got == bad {
				t.Errorf("query syntax %q kept as a term: %v", bad, terms)
			}
		}
	}
	want := map[string]bool{"easement": true, "survey": true, "plat": true, "vacated": true}
	for _, g := range terms {
		if !want[g] {
			t.Errorf("unexpected term %q in %v", g, terms)
		}
	}
}

// The earliest term present anchors the citation — a defensible anchor without
// asking FTS5 for per-term offsets.
func TestTheEarliestMatchingTermAnchorsTheCitation(t *testing.T) {
	spans := `[{"off":0,"page":1},{"off":10,"page":2}]`
	if got := HitPage(1, spans, "aaa bbbbb zzzzzzzzzz", "zzzzzzzzzz aaa"); got != 1 {
		t.Errorf("want the earliest term to anchor (page 1), got %d", got)
	}
}
