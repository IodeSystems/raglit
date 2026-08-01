package raglit

import (
	"context"
	"strings"
	"testing"
)

// A withdrawal without grounds is a delete with extra steps: the document is
// gone, nothing says why, and the next reader re-ingests it.
func TestWithdrawRequiresGrounds(t *testing.T) {
	s := testStore(t)
	if err := s.Withdraw(Withdrawal{Path: "/x.md"}); err == nil {
		t.Fatal("a withdrawal with no reason was accepted")
	}
	if err := s.Withdraw(Withdrawal{Path: "/x.md", Reason: "  "}); err == nil {
		t.Fatal("whitespace passed as grounds")
	}
}

// Withdrawing removes the document from the index and keeps the reason.
func TestWithdrawRemovesAndRemembers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "/drafts/letter.md", Title: "Letter",
		Fragments: []Fragment{{Text: "the Brannocks acted in good faith"}}}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search("good faith", 10); len(hits) == 0 {
		t.Fatal("premise broken: the draft is not searchable to begin with")
	}
	if err := s.Withdraw(Withdrawal{Path: "/drafts/letter.md",
		Reason: "own advocacy, not evidence", By: "carl"}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search("good faith", 10); len(hits) != 0 {
		t.Fatalf("a withdrawn document is still searchable: %d hit(s)", len(hits))
	}
	reason, ok := s.WithdrawnReason("/drafts/letter.md")
	if !ok || !strings.Contains(reason, "advocacy") {
		t.Fatalf("the grounds did not survive: %q, %v", reason, ok)
	}
}

// The ruling has to outlive a re-ingest, or the watcher undoes it on the next
// file change and the withdrawal lasts until somebody edits the document.
func TestWithdrawnDocumentIsNotReIngested(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Withdraw(Withdrawal{Path: "/drafts/letter.md", Reason: "own advocacy"}); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s, Fetcher: func(context.Context, string) (Fetched, error) {
		t.Fatal("a withdrawn document was fetched")
		return Fetched{}, nil
	}}
	if _, err := s.Enqueue("/drafts/letter.md", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if docs, _ := s.Documents(); len(docs) != 0 {
		t.Fatalf("a withdrawn document was re-indexed: %d document(s)", len(docs))
	}
}

// A withdrawal must not leave live citations pointing at a document nobody can
// retrieve. References are found by path and by file name, since a citation in
// this corpus is written either way.
func TestReferencesToFindsCitations(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "/packet.md", Fragments: []Fragment{
		{Text: "See the warm intro at documents/drafts/attorney-intro-warm.md for the framing."}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest(ctx, Document{Path: "/notes.md", Fragments: []Fragment{
		{Text: "Reworked attorney-intro-warm.md after the call."}}}); err != nil {
		t.Fatal(err)
	}
	// The document itself naming itself is not a dangling reference.
	if err := s.Ingest(ctx, Document{Path: "documents/drafts/attorney-intro-warm.md",
		Fragments: []Fragment{{Text: "attorney-intro-warm.md"}}}); err != nil {
		t.Fatal(err)
	}
	refs, err := s.ReferencesTo(ctx, "documents/drafts/attorney-intro-warm.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 references, got %d: %+v", len(refs), refs)
	}
	for _, r := range refs {
		if r.From == "documents/drafts/attorney-intro-warm.md" {
			t.Error("a document citing its own name was reported as a reference")
		}
		if r.Excerpt == "" {
			t.Errorf("reference from %s has no excerpt, so the claim it supports is invisible", r.From)
		}
	}
}
