package raglit

import (
	"context"
	"errors"
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

// Enqueue refuses node_modules on every index and whatever the index's own
// list names, with ErrIgnored so a batch can skip it.
func TestEnqueueRefusesIgnoredPaths(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Enqueue("/w/.claude/worktrees/a/mobile/node_modules/x/README.md", ""); !errors.Is(err, ErrIgnored) {
		t.Fatalf("node_modules enqueued: %v", err)
	}
	if _, err := s.Enqueue("/w/.claude/worktrees/a/README.md", ""); err != nil {
		t.Fatalf("an unignored path was refused: %v", err)
	}
	got, err := s.SetIgnorePatterns([]string{" **/.claude/** ", "*.lock", "", "*.lock"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("patterns = %q, want trimmed and deduplicated", got)
	}
	for _, p := range []string{"/w/.claude/worktrees/a/main.go", "file:///w/yarn.lock"} {
		if _, err := s.Enqueue(p, ""); !errors.Is(err, ErrIgnored) {
			t.Errorf("%s enqueued: %v", p, err)
		}
	}
	if _, err := s.Enqueue("/w/src/main.go", ""); err != nil {
		t.Errorf("an unignored path was refused: %v", err)
	}
	if got := s.IgnorePatterns(); len(got) != 2 || got[0] != "**/.claude/**" {
		t.Errorf("stored patterns = %q", got)
	}
}
