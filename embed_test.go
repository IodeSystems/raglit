package raglit

import (
	"context"
	"math"
	"strings"
	"testing"
)

// fakeEmbedder returns a deterministic 3-d vector keyed on which topic words a
// text contains — enough to make cosine ranking observable without a network.
type fakeVecClient struct{ calls int }

func (c *fakeVecClient) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	c.calls++
	out := make([][]float32, len(input))
	for i, t := range input {
		t = strings.ToLower(t)
		// axes: [auth, deploy, billing]
		v := []float32{0, 0, 0}
		if strings.Contains(t, "token") || strings.Contains(t, "auth") || strings.Contains(t, "refresh") {
			v[0] = 1
		}
		if strings.Contains(t, "deploy") || strings.Contains(t, "rollback") {
			v[1] = 1
		}
		if strings.Contains(t, "invoice") || strings.Contains(t, "billing") {
			v[2] = 1
		}
		out[i] = v
	}
	return out, nil
}

func TestVecSearch_RanksByCosine(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()

	must(t, s.Ingest(ctx, Document{Path: "auth.md", Title: "Auth", Fragments: []Fragment{
		{Page: 1, Text: "access token refresh flow"},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "deploy.md", Title: "Deploy", Fragments: []Fragment{
		{Page: 1, Text: "blue green deploy rollback"},
	}}))

	hits, err := s.VecSearch(ctx, "how does token refresh work", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no vector hits")
	}
	if hits[0].Path != "auth.md" {
		t.Fatalf("cosine ranked wrong doc first: %+v", hits[0])
	}
	// The auth vector aligns with the query axis → cosine ≈ 1.
	if math.Abs(hits[0].Score-1) > 1e-5 {
		t.Errorf("expected cosine ≈ 1 for aligned vectors, got %v", hits[0].Score)
	}
}

func TestHybridSearch_FusesLexicalAndVector(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "auth.md", Title: "Auth", Fragments: []Fragment{
		{Page: 1, Text: "access token refresh rotates"},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "deploy.md", Title: "Deploy", Fragments: []Fragment{
		{Page: 1, Text: "blue green deploy rollback"},
	}}))
	hits, err := s.HybridSearch(ctx, "token refresh", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "auth.md" {
		t.Fatalf("hybrid did not rank auth first: %+v", hits)
	}
}

func TestVecSearch_RequiresEmbedder(t *testing.T) {
	s := openMem(t)
	if _, err := s.VecSearch(context.Background(), "q", 5); err == nil {
		t.Fatal("VecSearch without an embedder should error")
	}
}

func TestEncodeDecodeVec_RoundTrips(t *testing.T) {
	v := []float32{0.1, -0.5, 1.0, 0}
	got := decodeVec(encodeVec(v))
	if len(got) != len(v) {
		t.Fatalf("len %d != %d", len(got), len(v))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("element %d: %v != %v", i, got[i], v[i])
		}
	}
}
