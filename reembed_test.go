package raglit

import (
	"context"
	"testing"
)

// A new index adopts the configured model; a different model drops the
// vectors, and the backfill rebuilds them from the stored text.
func TestReembed_ModelChangeDropsAndRebuilds(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "old"))
	must(t, s.Ingest(ctx, Document{Path: "file:///a.md", Title: "A",
		Fragments: []Fragment{{Text: "token refresh"}, {Text: "deploy rollback"}}}))
	if miss, _ := s.MissingVectors(); miss != 0 {
		t.Fatalf("after ingest with an embedder: %d missing", miss)
	}

	if dropped, err := s.SyncEmbedModel("old"); err != nil || dropped {
		t.Fatalf("first record: dropped=%v err=%v, want adopt", dropped, err)
	}
	if dropped, _ := s.SyncEmbedModel("old"); dropped {
		t.Fatal("same model must not drop vectors")
	}
	dropped, err := s.SyncEmbedModel("new")
	if err != nil || !dropped {
		t.Fatalf("model change: dropped=%v err=%v", dropped, err)
	}
	if miss, _ := s.MissingVectors(); miss != 2 {
		t.Fatalf("after the change %d missing, want 2", miss)
	}

	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "new"))
	n, err := s.BackfillVectors(ctx, 1)
	if err != nil || n != 1 {
		t.Fatalf("first batch: %d, %v", n, err)
	}
	for {
		n, err := s.BackfillVectors(ctx, 64)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if miss, _ := s.MissingVectors(); miss != 0 {
		t.Fatalf("after backfill %d missing", miss)
	}
	hits, err := s.VecSearch(ctx, "rotate the auth token", 1)
	if err != nil || len(hits) == 0 || hits[0].Text != "token refresh" {
		t.Fatalf("search after re-embed: %+v %v", hits, err)
	}
}
