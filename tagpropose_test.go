package raglit

import (
	"context"
	"testing"
)

// TestProposeTagMerges_SemanticPairsProposed covers spec 1: tags that are
// semantically equivalent but lexically distinct (sharing no significant word)
// get a merge candidate from the model pass, carrying the model's rationale.
func TestProposeTagMerges_SemanticPairsProposed(t *testing.T) {
	s := storeWithDocs(t, 2)
	// "escrow closing" and "settlement" share no word; the model sees both.
	tagged(t, s, "/corpus/scan_000.pdf", "agreement", []string{"escrow closing", "title insurance"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "agreement", []string{"settlement", "deed preparation"}, []string{"reference"})

	reply := `{"pairs":[{"a":"escrow closing","b":"settlement","rationale":"Both name the final step of a real estate transaction where funds are transferred and title passes."}]}`
	s.SetIdentifier(NewIdentifier(&identityChatter{replies: []string{reply}}, "m"))

	out, err := s.ProposeTagMerges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d proposals, want 1: %+v", len(out), out)
	}
	p := out[0]
	if p.A != "escrow closing" || p.B != "settlement" {
		t.Errorf("pair = (%q, %q), want (escrow closing, settlement)", p.A, p.B)
	}
	if p.Rationale == "" {
		t.Error("rationale is empty; the model's reason must be carried")
	}
	if p.Source != "model" {
		t.Errorf("source = %q, want model", p.Source)
	}
}

// TestProposeTagMerges_NoIdentifierSkipped covers spec 2: with no identity model
// configured, the model pass is skipped and returns no proposals. The lexical
// path (TagNeighbours) remains the sole mechanism.
func TestProposeTagMerges_NoIdentifierSkipped(t *testing.T) {
	s := storeWithDocs(t, 2)
	tagged(t, s, "/corpus/scan_000.pdf", "agreement", []string{"escrow closing"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "agreement", []string{"settlement"}, []string{"reference"})

	// No SetIdentifier call — the store has no identity model.
	out, err := s.ProposeTagMerges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("got %v, want nil (model pass skipped without an identifier)", out)
	}
	// The lexical path still works independently.
	near, err := s.TagNeighbours()
	if err != nil {
		t.Fatal(err)
	}
	// "escrow closing" and "settlement" share no word, so TagNeighbours has no
	// pair — that is correct: the lexical path simply has nothing to say here.
	if len(near) != 0 {
		t.Errorf("TagNeighbours = %v, want empty (no shared words)", near)
	}
}

// TestProposeTagMerges_NoDuplicationWithLexical covers spec 3: a pair the lexical
// path already surfaced (tags sharing a significant word) is NOT proposed again
// by the model pass, even if the model returns it.
func TestProposeTagMerges_NoDuplicationWithLexical(t *testing.T) {
	s := storeWithDocs(t, 2)
	// "lead paint" and "lead paint disclosure" share "lead" and "paint" —
	// the lexical path surfaces this pair.
	tagged(t, s, "/corpus/scan_000.pdf", "agreement", []string{"lead paint", "escrow closing"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "agreement", []string{"lead paint disclosure", "settlement"}, []string{"reference"})

	// The model proposes BOTH pairs: one the lexical path already has
	// (lead paint / lead paint disclosure) and one it does not
	// (escrow closing / settlement).
	reply := `{"pairs":[
		{"a":"lead paint","b":"lead paint disclosure","rationale":"The disclosure is about the same subject."},
		{"a":"escrow closing","b":"settlement","rationale":"Both name the final transfer step."}
	]}`
	s.SetIdentifier(NewIdentifier(&identityChatter{replies: []string{reply}}, "m"))

	out, err := s.ProposeTagMerges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only the non-lexical pair should survive: escrow closing / settlement.
	if len(out) != 1 {
		t.Fatalf("got %d proposals, want 1 (lexical pair must be dropped): %+v", len(out), out)
	}
	p := out[0]
	// The surviving pair is the semantic one.
	if (p.A != "escrow closing" || p.B != "settlement") && (p.A != "settlement" || p.B != "escrow closing") {
		t.Errorf("surviving pair = (%q, %q), want (escrow closing, settlement)", p.A, p.B)
	}
	// Verify the lexical pair is indeed surfaced by TagNeighbours.
	near, err := s.TagNeighbours()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for a, bset := range near {
		for _, b := range bset {
			if (a == "lead paint" && b == "lead paint disclosure") || (a == "lead paint disclosure" && b == "lead paint") {
				found = true
			}
		}
	}
	if !found {
		t.Error("TagNeighbours did not surface (lead paint, lead paint disclosure); test premise broken")
	}
}

// TestProposeTagMerges_EmptyCorpus covers the degenerate case: fewer than two
// tags means there is nothing to pair.
func TestProposeTagMerges_EmptyCorpus(t *testing.T) {
	s := storeWithDocs(t, 1)
	tagged(t, s, "/corpus/scan_000.pdf", "agreement", []string{"lead paint"}, []string{"reference"})

	reply := `{"pairs":[]}`
	s.SetIdentifier(NewIdentifier(&identityChatter{replies: []string{reply}}, "m"))

	out, err := s.ProposeTagMerges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("got %v, want nil for a single-tag corpus", out)
	}
}
