package raglit

import (
	"context"
	"testing"
)

// TestTagMergeProposal_AcceptAppliesMergeAndMarksAccepted covers F46: a person
// accepts a proposed merge. The merge is applied through the existing
// MergeTags mechanism and the proposal is marked accepted.
func TestTagMergeProposal_AcceptAppliesMergeAndMarksAccepted(t *testing.T) {
	s := storeWithDocs(t, 2)
	ctx := context.Background()
	tagged(t, s, "/corpus/scan_000.pdf", "agreement", []string{"escrow closing"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "agreement", []string{"settlement"}, []string{"reference"})

	// The model pass produced one proposal: escrow closing → settlement.
	ids, err := s.RecordTagMergeProposals(ctx, []TagMergeProposal{
		{A: "escrow closing", B: "settlement", Rationale: "Both name the final step of a transaction.", Source: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("recorded %d proposals, want 1", len(ids))
	}
	id := ids[0]

	// Pending before the ruling.
	recs, err := s.ListTagMergeProposals(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Status != "pending" {
		t.Fatalf("pending = %+v, want one pending", recs)
	}

	got, err := s.AcceptTagMergeProposal(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "accepted" {
		t.Errorf("status = %q, want accepted", got.Status)
	}
	if got.Result == "" {
		t.Error("result is empty; the TagMergeResult must be recorded")
	}

	// The merge ran through the existing mechanism: the From tag collapsed into
	// To.
	ident, err := s.DocumentIdentity("/corpus/scan_000.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(ident.ContentTags) != 1 || ident.ContentTags[0] != "settlement" {
		t.Errorf("tags after accept = %v, want one 'settlement'", ident.ContentTags)
	}

	// No longer pending.
	pending, err := s.ListTagMergeProposals(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending after accept = %d, want 0", len(pending))
	}
	accepted, err := s.ListTagMergeProposals(ctx, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0].ID != id {
		t.Errorf("accepted = %+v, want the one proposal", accepted)
	}

	// A second ruling on the same id is refused: it is no longer pending.
	if _, err := s.AcceptTagMergeProposal(ctx, id); err == nil {
		t.Error("accepting an already-accepted proposal succeeded, want an error")
	}
}

// TestTagMergeProposal_RejectLeavesTagsUntouchedAndMarksRejected covers F46: a
// person rejects a proposed merge. Both tags remain unchanged and the proposal
// is marked rejected.
func TestTagMergeProposal_RejectLeavesTagsUntouchedAndMarksRejected(t *testing.T) {
	s := storeWithDocs(t, 2)
	ctx := context.Background()
	tagged(t, s, "/corpus/scan_000.pdf", "agreement", []string{"escrow closing"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "agreement", []string{"settlement"}, []string{"reference"})

	ids, err := s.RecordTagMergeProposals(ctx, []TagMergeProposal{
		{A: "escrow closing", B: "settlement", Rationale: "Both name the final step.", Source: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]

	got, err := s.RejectTagMergeProposal(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "rejected" {
		t.Errorf("status = %q, want rejected", got.Status)
	}

	// Both tags remain: the reject touched nothing.
	one, err := s.DocumentIdentity("/corpus/scan_000.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.ContentTags) != 1 || one.ContentTags[0] != "escrow closing" {
		t.Errorf("tags after reject = %v, want 'escrow closing' unchanged", one.ContentTags)
	}
	two, err := s.DocumentIdentity("/corpus/scan_001.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(two.ContentTags) != 1 || two.ContentTags[0] != "settlement" {
		t.Errorf("tags after reject = %v, want 'settlement' unchanged", two.ContentTags)
	}

	rejected, err := s.ListTagMergeProposals(ctx, "rejected")
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 || rejected[0].ID != id {
		t.Errorf("rejected = %+v, want the one proposal", rejected)
	}
	pending, _ := s.ListTagMergeProposals(ctx, "pending")
	if len(pending) != 0 {
		t.Errorf("pending after reject = %d, want 0", len(pending))
	}

	// A second ruling is refused.
	if _, err := s.RejectTagMergeProposal(ctx, id); err == nil {
		t.Error("rejecting an already-rejected proposal succeeded, want an error")
	}
}

// TestTagMergeProposal_RecordIsIdempotentPerPair: re-recording the same pair
// does not duplicate it and does not re-open a row a person has already ruled
// on.
func TestTagMergeProposal_RecordIsIdempotentPerPair(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	tagged(t, s, "/corpus/scan_000.pdf", "agreement", []string{"escrow closing"}, []string{"reference"})

	ids, err := s.RecordTagMergeProposals(ctx, []TagMergeProposal{
		{A: "escrow closing", B: "settlement", Source: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectTagMergeProposal(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}

	// Re-record the same pair: the existing (rejected) row is kept, not a new
	// pending one.
	if _, err := s.RecordTagMergeProposals(ctx, []TagMergeProposal{
		{A: "escrow closing", B: "settlement", Source: "model"},
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListTagMergeProposals(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d rows, want 1 (idempotent): %+v", len(all), all)
	}
	if all[0].Status != "rejected" {
		t.Errorf("status = %q, want the existing 'rejected' kept", all[0].Status)
	}
}
