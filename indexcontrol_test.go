package raglit

import (
	"context"
	"testing"
)

// A paused index claims nothing: ingest and caption jobs wait, and so does a
// re-embed. Enqueueing still works, and resume releases the queue.
func TestPausedIndexClaimsNothing(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "m"))
	if err := s.Ingest(context.Background(), Document{Path: "a.md", Title: "A",
		Fragments: []Fragment{{Text: "token refresh"}}}); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetPaused(true))
	if !s.Paused() {
		t.Fatal("not paused after SetPaused(true)")
	}
	if _, err := s.Enqueue("/x/b.md", ""); err != nil {
		t.Fatalf("enqueue while paused: %v", err)
	}
	if j, err := s.ClaimNext(); err != nil || j != nil {
		t.Fatalf("paused index claimed an ingest job: %+v %v", j, err)
	}
	if _, err := s.EnqueueIdentity("a.md", false); err != nil {
		t.Fatal(err)
	}
	if j, err := s.claimNextIdentityJob(); err != nil || j != nil {
		t.Fatalf("paused index claimed a caption job: %+v %v", j, err)
	}
	if _, err := s.db.Exec(`DELETE FROM fragment_vectors`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.BackfillVectors(context.Background(), 8); err != nil || n != 0 {
		t.Fatalf("paused index re-embedded %d: %v", n, err)
	}

	must(s.SetPaused(false))
	if j, err := s.ClaimNext(); err != nil || j == nil {
		t.Fatalf("resumed index claimed nothing: %v", err)
	}
	if j, err := s.claimNextIdentityJob(); err != nil || j == nil {
		t.Fatalf("resumed index claimed no caption job: %v", err)
	}
}
