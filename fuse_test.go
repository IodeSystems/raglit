package raglit

import "testing"

// A document that both rankings find, through DIFFERENT fragments, is stronger
// evidence than a document only one ranking finds. Fusing per fragment never
// combined the two, so the lexical-only document won; measured on GitLab's
// history (bench/retrieval), fusing per document lifts MRR.
func TestFuseRRFReinforcesADocumentBothRankingsFind(t *testing.T) {
	lex := []Hit{
		{ID: 10, Path: "b.go"}, // rank 0, lexical only
		{ID: 1, Path: "a.go"},  // rank 1, a.go fragment 1
	}
	vec := []Hit{
		{ID: 30, Path: "c.go"}, // rank 0, vector only
		{ID: 2, Path: "a.go"},  // rank 1, a.go fragment 2
	}
	got := fuseRRF(lex, vec, 10)
	if len(got) == 0 || got[0].Path != "a.go" {
		t.Fatalf("first hit = %+v, want a.go (found by both rankings)", got)
	}
	// Every fragment is still returned, and a document's lead fragment comes
	// before its other fragments.
	if len(got) != 4 {
		t.Fatalf("got %d hits, want all 4 fragments", len(got))
	}
	seen := map[string]bool{}
	for _, h := range got {
		if h.Path == "a.go" && seen["a.go"] && h.Score > got[0].Score {
			t.Fatalf("a.go's second fragment outranks its lead: %+v", got)
		}
		seen[h.Path] = true
	}
}
