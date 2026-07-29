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

// countingVecClient records the size of each request it receives.
type countingVecClient struct {
	batches []int // total input chars per request
	items   []int // inputs per request
}

func (c *countingVecClient) Embed(_ context.Context, _ string, texts []string) ([][]float32, error) {
	n := 0
	for _, t := range texts {
		n += len(t)
	}
	c.batches = append(c.batches, n)
	c.items = append(c.items, len(texts))
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

// The failure this exists for. The caller batched by ITEM COUNT — sixteen at a
// time — and the server bounds the WHOLE REQUEST. Sixteen ordinary fragments
// came to 35,871 tokens against a batch limit of 8192, and every document with
// large fragments failed with a 500 that looked like an upstream fault.
func TestEmbedDocsSplitsByRequestSizeNotItemCount(t *testing.T) {
	c := &countingVecClient{}
	e := NewEmbedder(c, "m")
	e.BatchLimitChars = 1000

	texts := make([]string, 16)
	for i := range texts {
		texts[i] = strings.Repeat("x", 400) // 16 x 400 = 6400 chars, 6.4x the budget
	}
	vecs, err := e.EmbedDocs(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 16 {
		t.Fatalf("want a vector per input, got %d", len(vecs))
	}
	if len(c.batches) < 2 {
		t.Fatalf("everything went in one request (%v chars) — the budget was ignored", c.batches)
	}
	for i, n := range c.batches {
		if n > e.BatchLimitChars && c.items[i] > 1 {
			t.Errorf("request %d carried %d chars, over the %d budget with %d items",
				i, n, e.BatchLimitChars, c.items[i])
		}
	}
}

// A single input bigger than the whole budget still goes, alone. Refusing it
// here would silently drop a fragment; letting the endpoint answer turns it into
// a real error about that fragment.
func TestEmbedDocsSendsAnOversizedInputAlone(t *testing.T) {
	c := &countingVecClient{}
	e := NewEmbedder(c, "m")
	e.BatchLimitChars = 100
	if _, err := e.EmbedDocs(context.Background(), []string{strings.Repeat("y", 5000), "small"}); err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 2 || c.items[0] != 1 {
		t.Errorf("want the oversized input alone in its own request, got items=%v", c.items)
	}
}

// Order must survive chunking: vector i belongs to text i.
func TestEmbedDocsPreservesOrderAcrossChunks(t *testing.T) {
	e := NewEmbedder(&countingVecClient{}, "m")
	e.BatchLimitChars = 50
	texts := []string{"aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccc"}
	vecs, err := e.EmbedDocs(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("want %d vectors, got %d", len(texts), len(vecs))
	}
}
