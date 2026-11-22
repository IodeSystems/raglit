package raglit

import (
	"context"
	"testing"
)

// TestAnalyze_DumpsFragmentsAsStored proves that Analyze returns the
// document's own fragments with their stored fields (text, ord, page,
// page_spans, origin) intact, and that the identity fragment is identified
// separately.
func TestAnalyze_DumpsFragmentsAsStored(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	const path = "/corpus/0428_001.pdf"

	// Add the identity so there is one to dump.
	if err := s.SetDocumentIdentity(ctx, path, machineIdentity()); err != nil {
		t.Fatal(err)
	}

	rep, err := Analyze(ctx, s, path)
	if err != nil {
		t.Fatal(err)
	}

	// Fragments as stored: the two body fragments, in page/ord order.
	if len(rep.Fragments) != 2 {
		t.Fatalf("fragments = %d, want 2", len(rep.Fragments))
	}
	if rep.Fragments[0].Page != 1 || rep.Fragments[0].Ord != 0 {
		t.Errorf("frag[0] page/ord = %d/%d, want 1/0", rep.Fragments[0].Page, rep.Fragments[0].Ord)
	}
	if rep.Fragments[0].Text != psaText {
		t.Errorf("frag[0] text = %q, want psaText", rep.Fragments[0].Text[:40])
	}
	if rep.Fragments[1].Page != 2 || rep.Fragments[1].Ord != 0 {
		t.Errorf("frag[1] page/ord = %d/%d, want 2/0", rep.Fragments[1].Page, rep.Fragments[1].Ord)
	}
	if rep.Fragments[1].Text != "Signature page." {
		t.Errorf("frag[1] text = %q, want %q", rep.Fragments[1].Text, "Signature page.")
	}

	// Identity caption and summary are surfaced.
	if rep.Identity.Name == "" || rep.Identity.Summary == "" {
		t.Errorf("identity not surfaced: %+v", rep.Identity)
	}
	if rep.Identity.Name != machineIdentity().Name {
		t.Errorf("identity name = %q, want %q", rep.Identity.Name, machineIdentity().Name)
	}

	// The origin='identity' fragment is identified.
	if rep.IdentityFragment == nil {
		t.Fatal("identity fragment not identified")
	}
	if rep.IdentityFragment.Origin != fragOriginIdentity {
		t.Errorf("identity fragment origin = %q, want %q", rep.IdentityFragment.Origin, fragOriginIdentity)
	}
	// It is page 0, ord 0 (the convention for identity fragments).
	if rep.IdentityFragment.Page != 0 || rep.IdentityFragment.Ord != 0 {
		t.Errorf("identity fragment page/ord = %d/%d, want 0/0", rep.IdentityFragment.Page, rep.IdentityFragment.Ord)
	}
	// Identity fragments are deliberately not embedded.
	if rep.IdentityEmbedded {
		t.Error("identity fragment marked as embedded; it should not be")
	}

	// Doc identity is the path.
	if rep.DocIdentity != path {
		t.Errorf("doc identity = %q, want %q", rep.DocIdentity, path)
	}
}

// TestAnalyze_MissingDocumentErrors proves that Analyze returns an error when
// the document is not in the index.
func TestAnalyze_MissingDocumentErrors(t *testing.T) {
	s := storeWithDoc(t)
	_, err := Analyze(context.Background(), s, "/corpus/nope.pdf")
	if err == nil {
		t.Fatal("want an error for a missing document, got nil")
	}
}
