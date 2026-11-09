package raglit

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

	if dropped, err := s.SyncEmbedModel("old", 0); err != nil || dropped {
		t.Fatalf("first record: dropped=%v err=%v, want adopt", dropped, err)
	}
	if dropped, _ := s.SyncEmbedModel("old", 0); dropped {
		t.Fatal("same model must not drop vectors")
	}
	dropped, err := s.SyncEmbedModel("new", 0)
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

// fixedDimClient returns a vector of length dim for every input, with the
// first axis set when the text names a token. Stateless, so it is safe to call
// from several goroutines (fakeVecClient counts its calls and is not).
type fixedDimClient struct{ dim int }

func (c fixedDimClient) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for i, t := range input {
		v := make([]float32, c.dim)
		v[c.dim-1] = 1
		if strings.Contains(t, "token") {
			v[0] = 1
		}
		out[i] = v
	}
	return out, nil
}

// syncIndex is an index with two embedded fragments and model "m" recorded at
// dim 3.
func syncIndex(t *testing.T) *Store {
	t.Helper()
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(fixedDimClient{dim: 3}, "m"))
	must(t, s.Ingest(context.Background(), Document{Path: "file:///a.md", Title: "A",
		Fragments: []Fragment{{Text: "token refresh"}, {Text: "deploy rollback"}}}))
	if dropped, err := s.SyncEmbedModel("m", 3); err != nil || dropped {
		t.Fatalf("first record: dropped=%v err=%v, want adopt", dropped, err)
	}
	if v, _ := s.Meta(metaEmbedDim); v != "3" {
		t.Fatalf("embed_dim = %q, want 3", v)
	}
	return s
}

func TestSyncEmbedModel_Dim(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		dim      int
		dropped  bool
		wantDim  string
		wantMiss int
	}{
		{"same id, same dim keeps", "m", 3, false, "3", 0},
		{"same id, new dim drops", "m", 4, true, "4", 2},
		{"id change drops", "m2", 3, true, "3", 2},
		{"dim 0 keeps and leaves the record", "m", 0, false, "3", 0},
		// The recorded dim described the dropped vectors, so it goes with them.
		{"id change with dim 0 drops and forgets the dim", "m2", 0, true, "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := syncIndex(t)
			dropped, err := s.SyncEmbedModel(c.model, c.dim)
			if err != nil || dropped != c.dropped {
				t.Fatalf("dropped=%v err=%v, want %v", dropped, err, c.dropped)
			}
			if m := s.EmbedModel(); m != c.model {
				t.Fatalf("embed_model = %q, want %q", m, c.model)
			}
			if v, _ := s.Meta(metaEmbedDim); v != c.wantDim {
				t.Fatalf("embed_dim = %q, want %q", v, c.wantDim)
			}
			if miss, _ := s.MissingVectors(); miss != c.wantMiss {
				t.Fatalf("missing = %d, want %d", miss, c.wantMiss)
			}
		})
	}
}

// An index from before embed_dim was recorded: the stored vectors' length
// stands in for the record.
func TestSyncEmbedModel_NoDimRecordUsesStoredVectors(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(fixedDimClient{dim: 3}, "m"))
	must(t, s.Ingest(context.Background(), Document{Path: "file:///a.md", Title: "A",
		Fragments: []Fragment{{Text: "token refresh"}}}))
	must(t, s.SetMeta(metaEmbedModel, "m", 1))

	if dropped, err := s.SyncEmbedModel("m", 3); err != nil || dropped {
		t.Fatalf("matching length: dropped=%v err=%v, want keep", dropped, err)
	}
	if v, _ := s.Meta(metaEmbedDim); v != "3" {
		t.Fatalf("embed_dim = %q, want 3 recorded", v)
	}
	must(t, s.SetMeta(metaEmbedDim, "", 1)) // back to no usable record
	if dropped, err := s.SyncEmbedModel("m", 5); err != nil || !dropped {
		t.Fatalf("other length: dropped=%v err=%v, want drop", dropped, err)
	}
}

func TestDropVectors_KeepsFragmentsAndFTS(t *testing.T) {
	s := syncIndex(t)
	ctx := context.Background()
	frags := countRows(t, s, `SELECT COUNT(*) FROM fragments`)

	n, err := s.DropVectors()
	if err != nil || n != 2 {
		t.Fatalf("DropVectors = %d, %v; want 2", n, err)
	}
	if miss, _ := s.MissingVectors(); miss != 2 {
		t.Fatalf("missing = %d, want 2 (all)", miss)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM fragments`); got != frags {
		t.Fatalf("fragments = %d, want %d", got, frags)
	}
	if h, err := s.Search("rollback", 3); err != nil || len(h) != 1 {
		t.Fatalf("FTS after DropVectors: %d hits, %v", len(h), err)
	}
	if m, _ := s.Meta(metaEmbedDim); m != "3" || s.EmbedModel() != "m" {
		t.Fatalf("records changed: model=%q dim=%q", s.EmbedModel(), m)
	}
	for {
		k, err := s.BackfillVectors(ctx, 64)
		if err != nil {
			t.Fatal(err)
		}
		if k == 0 {
			break
		}
	}
	if miss, _ := s.MissingVectors(); miss != 0 {
		t.Fatalf("after backfill %d missing", miss)
	}
}

// R6d: SetEmbedder swaps the embedder while ingest and vector search read it.
// Meant for go test -race; without -race it still checks nothing panics.
func TestSetEmbedder_ConcurrentWithIngestAndSearch(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	a := NewEmbedder(fixedDimClient{dim: 3}, "m")
	b := NewEmbedder(fixedDimClient{dim: 3}, "m")
	s.SetEmbedder(a)
	must(t, s.Ingest(ctx, Document{Path: "file:///seed.md", Title: "Seed",
		Fragments: []Fragment{{Text: "token refresh"}}}))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			switch i % 3 {
			case 0:
				s.SetEmbedder(a)
			case 1:
				s.SetEmbedder(b)
			default:
				s.SetEmbedder(nil)
			}
		}
	}()
	for i := 0; i < 50; i++ {
		path := fmt.Sprintf("file:///d%d.md", i)
		if err := s.Ingest(ctx, Document{Path: path, Title: "D",
			Fragments: []Fragment{{Text: "token " + path}}}); err != nil {
			t.Fatal(err)
		}
		// nil at the moment of the call is a clean error, not a panic.
		_, _ = s.VecSearch(ctx, "token", 3)
		_, _ = s.HybridSearch(ctx, "token", 3)
		_, _ = s.BackfillVectors(ctx, 8)
	}
	close(stop)
	wg.Wait()
}

// gateClient is fixedDimClient whose first call signals entered and then waits
// for release: the test swaps the embedder while that call is in flight.
type gateClient struct {
	fixedDimClient
	once     sync.Once
	entered  chan struct{}
	released chan struct{}
}

func newGateClient(dim int) *gateClient {
	return &gateClient{fixedDimClient: fixedDimClient{dim: dim},
		entered: make(chan struct{}), released: make(chan struct{})}
}

func (c *gateClient) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	c.once.Do(func() {
		close(c.entered)
		<-c.released
	})
	return c.fixedDimClient.Embed(ctx, model, input)
}

func vectorDims(t *testing.T, s *Store) map[int]int {
	t.Helper()
	rows, err := s.db.Query(`SELECT dim, COUNT(*) FROM fragment_vectors GROUP BY dim`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var d, n int
		if err := rows.Scan(&d, &n); err != nil {
			t.Fatal(err)
		}
		out[d] = n
	}
	return out
}

// An ingest that embedded with the embedder a reembed replaced (SetEmbedder,
// then SyncEmbedModel) writes no vectors: they would be in the old space, and
// the backfill fills only fragments with none. The backfill then embeds the
// fragment with the new model.
func TestReembed_InFlightIngestLeavesNoOldVectors(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	old := newGateClient(3)
	s.SetEmbedder(NewEmbedder(old, "old"))
	if _, err := s.SyncEmbedModel("old", 3); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.Ingest(ctx, Document{Path: "file:///a.md", Title: "A",
			Fragments: []Fragment{{Text: "token refresh"}}})
	}()
	<-old.entered
	s.SetEmbedder(NewEmbedder(fixedDimClient{dim: 4}, "new"))
	if dropped, err := s.SyncEmbedModel("new", 4); err != nil || !dropped {
		t.Fatalf("model change: dropped=%v err=%v", dropped, err)
	}
	close(old.released)
	must(t, <-done)
	if miss, _ := s.MissingVectors(); miss != 1 {
		t.Fatalf("after the in-flight ingest %d missing, want 1 (old vector discarded); dims %v", miss, vectorDims(t, s))
	}
	if n, err := s.BackfillVectors(ctx, 8); err != nil || n != 1 {
		t.Fatalf("backfill: %d, %v", n, err)
	}
	if got := vectorDims(t, s); len(got) != 1 || got[4] != 1 {
		t.Fatalf("vector dims %v, want one of dim 4", got)
	}
}

// The same for a backfill batch in flight across the swap: its vectors are
// discarded and it says so, rather than reporting the batch as embedded.
func TestReembed_InFlightBackfillIsDiscarded(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "file:///a.md", Title: "A",
		Fragments: []Fragment{{Text: "token refresh"}}}))
	old := newGateClient(3)
	s.SetEmbedder(NewEmbedder(old, "old"))
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := s.BackfillVectors(ctx, 8)
		done <- result{n, err}
	}()
	<-old.entered
	s.SetEmbedder(NewEmbedder(fixedDimClient{dim: 4}, "new"))
	close(old.released)
	if r := <-done; r.n != 0 || r.err != errEmbedderChanged {
		t.Fatalf("stale backfill: %d, %v; want 0, errEmbedderChanged", r.n, r.err)
	}
	if miss, _ := s.MissingVectors(); miss != 1 {
		t.Fatalf("%d missing, want 1", miss)
	}
}

// commitDoc (the staged pipeline and the pool path) drops the vectors of an
// embedder that is no longer the store's, fragment and description alike, and
// keeps an image-space figure vector, which the text embedder did not make.
func TestCommitDoc_StaleEmbedderVectorsDropped(t *testing.T) {
	s := openMem(t)
	a := NewEmbedder(fixedDimClient{dim: 3}, "a")
	s.SetEmbedder(NewEmbedder(fixedDimClient{dim: 4}, "b"))
	frags := []stagedFrag{{page: 1, ord: 0, text: "token refresh"}}
	media := []stagedMedia{
		{page: 1, kind: "figure", description: "d1", fragIdx: 0, vec: []float32{1, 0, 0}, space: spaceText},
		{page: 1, ord: 1, kind: "figure", description: "d2", fragIdx: 0, vec: []float32{1, 0}, space: spaceImage},
	}
	must(t, s.commitDoc("file:///a.md", "A", "overlap", "", frags, nil, media,
		map[int][]float32{0: {1, 0, 0}}, &embedStamp{emb: a}, nil))
	if miss, _ := s.MissingVectors(); miss != 1 {
		t.Fatalf("%d missing, want 1", miss)
	}
	var spaces []string
	rows, err := s.db.Query(`SELECT space FROM media_vectors`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sp string
		must(t, rows.Scan(&sp))
		spaces = append(spaces, sp)
	}
	if len(spaces) != 1 || spaces[0] != spaceImage {
		t.Fatalf("media vector spaces %v, want only %q", spaces, spaceImage)
	}
}
