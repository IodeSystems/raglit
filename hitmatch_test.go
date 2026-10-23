package raglit

import (
	"context"
	"reflect"
	"testing"
)

// TestHitMeasures covers the per-hit measures a caller thresholds on: the
// raw cosine survives hybrid's rank fusion, and term coverage follows FTS5's
// stemming and ignores stopwords.
func TestHitMeasures(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "/d/restart.md", Fragments: []Fragment{
		{Text: "the daemon restarts and every session reconnects"},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "/d/other.md", Fragments: []Fragment{
		{Text: "a daemon writes logs"},
	}}))

	q := "how does the daemon restart affect a session"
	h, err := s.SearchPath(q, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, x := range h {
		got[x.Path] = x.Matched
		if x.QueryTerms != 4 { // daemon restart affect session
			t.Errorf("%s: query_terms = %d, want 4", x.Path, x.QueryTerms)
		}
		if x.Similarity != nil {
			t.Errorf("%s: bm25 hit has similarity", x.Path)
		}
	}
	want := map[string][]string{
		"/d/restart.md": {"daemon", "restart", "session"},
		"/d/other.md":   {"daemon"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matched = %v, want %v", got, want)
	}

	v, err := s.VecSearchPath(ctx, q, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range v {
		if x.Similarity == nil || *x.Similarity != x.Score {
			t.Fatalf("vec %s: similarity %v, score %v", x.Path, x.Similarity, x.Score)
		}
	}
	hy, err := s.HybridSearchPath(ctx, q, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range hy {
		if x.Similarity == nil || *x.Similarity == x.Score {
			t.Fatalf("hybrid %s: similarity %v should be the cosine, not the fused score %v", x.Path, x.Similarity, x.Score)
		}
		if x.Matched == nil {
			t.Fatalf("hybrid %s: no matched terms", x.Path)
		}
	}
}

func TestSearchTerms(t *testing.T) {
	got := searchTerms(`What's the "daemon"? Daemon restart, the end.`)
	want := []string{"what's", "daemon", "restart", "end"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("searchTerms = %q, want %q", got, want)
	}
}
