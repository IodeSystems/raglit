package raglit

import (
	"context"
	"path/filepath"
	"testing"
)

func openScopedIndex(t *testing.T, root, name string) *Store {
	t.Helper()
	s, err := OpenHome(Home(filepath.Join(root, "indexes", name)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestPool_CrossIndexReuse proves the shared pool eliminates duplicate indexing
// work: index A processes a doc (with embeddings); index B reuses it from the
// pool with NO embedder — yet gets the same fragments AND cached vectors.
func TestPool_CrossIndexReuse(t *testing.T) {
	root := t.TempDir()
	pool, err := OpenPool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	const recipe, file = "recipe-v1", "filehash-x"

	// Index A: the expensive path (embed via the fake client).
	a := openScopedIndex(t, root, "a")
	a.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	must(t, a.Ingest(ctx, Document{Path: "file:///x.md", Title: "X", Fragments: []Fragment{
		{Page: 1, Ord: 0, Text: "alpha zebra"},
		{Page: 2, Ord: 0, Text: "beta walrus"},
	}}))
	doc, err := a.ExportDoc("file:///x.md")
	must(t, err)
	if len(doc.Fragments) != 2 || len(doc.Fragments[0].Vec) == 0 {
		t.Fatalf("export = %d frags, first vec len %d (want 2 frags with vectors)", len(doc.Fragments), len(doc.Fragments[0].Vec))
	}
	must(t, pool.Put(recipe, file, doc))

	// Index B: reuse from the pool, no embedder configured → no re-embed work.
	b := openScopedIndex(t, root, "b")
	got, ok, err := pool.Get(recipe, file)
	must(t, err)
	if !ok {
		t.Fatal("pool miss after Put")
	}
	n, err := b.IngestPooled(ctx, "file:///x.md", got.Title, got, pool.FileDir(file))
	must(t, err)
	if n != 2 {
		t.Fatalf("IngestPooled indexed %d fragments, want 2", n)
	}
	// B is searchable and carries the cached vectors despite having no embedder.
	if h, _ := b.Search("zebra", 5); len(h) != 1 {
		t.Fatalf("B should find pooled content: %d hits", len(h))
	}
	var vecs int
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM fragment_vectors`).Scan(&vecs); err != nil {
		t.Fatal(err)
	}
	if vecs != 2 {
		t.Fatalf("B vectors reused from pool = %d, want 2 (no re-embed)", vecs)
	}
	// A miss for a different recipe (alt model) — reprocess, not reuse.
	if _, ok, _ := pool.Get("recipe-v2", file); ok {
		t.Fatal("different recipe must be a pool miss")
	}
}

func TestPool_GCEvictsLRU(t *testing.T) {
	root := t.TempDir()
	pool, err := OpenPool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	put := func(file string) {
		must(t, pool.Put("recipe", file, PooledDoc{Fragments: []PooledFragment{{Text: file}}}))
	}
	put("f1")
	put("f2")
	put("f3")
	if st, _ := pool.Stats(); st.Entries != 3 {
		t.Fatalf("entries = %d, want 3", st.Entries)
	}
	// Bump f3 and f1 to most-recently-used; f2 is now the LRU.
	pool.Get("recipe", "f3")
	pool.Get("recipe", "f1")

	// Nothing to do when both budgets are disabled.
	if n, _ := pool.GC(0, 0); n != 0 {
		t.Fatalf("GC(0,0) evicted %d, want 0", n)
	}
	// Cap at 2 → evict the single LRU entry (f2).
	n, err := pool.GC(0, 2)
	must(t, err)
	if n != 1 {
		t.Fatalf("GC evicted %d, want 1", n)
	}
	if _, ok, _ := pool.Get("recipe", "f2"); ok {
		t.Fatal("f2 (LRU) should have been evicted")
	}
	for _, keep := range []string{"f1", "f3"} {
		if _, ok, _ := pool.Get("recipe", keep); !ok {
			t.Fatalf("%s should have survived GC", keep)
		}
	}
	if st, _ := pool.Stats(); st.Entries != 2 {
		t.Fatalf("entries after GC = %d, want 2", st.Entries)
	}
}
