package raglit

import "testing"

func folded(pages ...string) FoldedText {
	ps := make([]PageText, len(pages))
	for i, p := range pages {
		ps[i] = PageText{Page: i + 1, Text: p}
	}
	return FoldPages(ps)
}

// The distinction the whole file exists for. Both of these average to roughly
// the same whole-document number, and they mean opposite things.
func TestShapeSeparatesARefilingFromARescan(t *testing.T) {
	refiling := Shape([]PagePair{
		{APage: 1, BPage: 1, Rate: 1}, {APage: 2, BPage: 2, Rate: 1},
		{APage: 3, BPage: 3, Rate: 0.2}, {APage: 4, BPage: 4, Rate: 1},
	})
	if refiling.Clean != 3 || refiling.Divergent != 1 {
		t.Errorf("localised disagreement misread: %+v", refiling)
	}
	rescan := Shape([]PagePair{
		{APage: 1, BPage: 1, Rate: 0.8}, {APage: 2, BPage: 2, Rate: 0.75},
		{APage: 3, BPage: 3, Rate: 0.82}, {APage: 4, BPage: 4, Rate: 0.78},
	})
	if rescan.Noisy != 4 || rescan.Clean != 0 || rescan.Divergent != 0 {
		t.Errorf("diffuse disagreement misread: %+v", rescan)
	}
}

// A page present in one document and absent from the other is the strongest
// per-page finding available, and no average can show it.
func TestAPageMissingFromBIsReported(t *testing.T) {
	a := folded("the quick brown fox jumps over the lazy dog again and again and again",
		"a wholly unrelated page about surveying and easements in havern county")
	b := folded("the quick brown fox jumps over the lazy dog again and again and again")

	pages, onlyB := PageRates(a, b, 8)
	if len(pages) != 2 {
		t.Fatalf("want a row per page of A, got %d", len(pages))
	}
	if pages[0].Rate < 0.9 {
		t.Errorf("the shared page should match strongly, got %.3f", pages[0].Rate)
	}
	if pages[1].BPage != 0 || pages[1].Rate != 0 {
		t.Errorf("page 2 is not in B and must say so: %+v", pages[1])
	}
	if len(onlyB) != 0 {
		t.Errorf("B has no unmatched pages, got %v", onlyB)
	}
	if s := Shape(pages); s.Missing != 1 {
		t.Errorf("want 1 missing, got %+v", s)
	}
}

// Pages only in B are the other half of "what is missing" and a table keyed on
// A cannot show them.
func TestPagesOnlyInBAreReported(t *testing.T) {
	a := folded("the quick brown fox jumps over the lazy dog again and again and again")
	b := folded("the quick brown fox jumps over the lazy dog again and again and again",
		"an addendum that exists only in the executed copy of this agreement")

	_, onlyB := PageRates(a, b, 8)
	if len(onlyB) != 1 || onlyB[0] != 2 {
		t.Errorf("want page 2 of B unmatched, got %v", onlyB)
	}
}

// Identical is a stronger claim than a rate of 1.0: shingles can be a subset
// while the text differs in what falls between them.
func TestIdenticalIsExactNotJustRateOne(t *testing.T) {
	same := "an identical page of text repeated exactly in both documents here"
	pages, _ := PageRates(folded(same), folded(same), 8)
	if !pages[0].Identical {
		t.Error("the same text must report Identical")
	}
	if pages[0].Rate < 0.999 {
		t.Errorf("and a full rate, got %.3f", pages[0].Rate)
	}
}
