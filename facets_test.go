package raglit

import (
	"context"
	"testing"
)

// facetFixture builds a small corpus where every facet column is exercised:
//  - kind:      "guide" (a.md), "reference" (b.md), "" (c.md)
//  - role_tag:  "documentation" (a.md), "reference" (b.md), "" (c.md)
//  - content_tag: "deploy" (a.md), "rollback" (b.md), "" (c.md)
//  - doc_type:  "runbook" (a.md), "" (b.md and c.md)
//  - origin:    "described" on the extra fragment in a.md only
//
// All three documents match the query "token", so FacetCounts over "token"
// returns counts for every non-empty facet value.
func facetFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "a.md", Title: "A", Fragments: []Fragment{
		{Text: "token one"},
		{Text: "described text", Page: 1},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "b.md", Title: "B", Fragments: []Fragment{
		{Text: "token two"},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "c.md", Title: "C", Fragments: []Fragment{
		{Text: "token three"},
	}}))
	must(t, s.SetDocumentIdentity(ctx, "a.md", DocIdentity{
		Kind:        "guide",
		RoleTags:    []string{"documentation"},
		ContentTags: []string{"deploy"},
		DocType:     "runbook",
	}))
	must(t, s.SetDocumentIdentity(ctx, "b.md", DocIdentity{
		Kind:        "reference",
		RoleTags:    []string{"reference"},
		ContentTags: []string{"rollback"},
	}))
	// Set a described origin on a.md's first fragment (the one that matches "token").
	if _, err := s.db.Exec(`UPDATE fragments SET origin='described' WHERE doc_id=(SELECT id FROM documents WHERE path='a.md') AND page=0`); err != nil {
		t.Fatal(err)
	}
}

func facetByKeyVal(facets []Facet) map[string]int {
	m := map[string]int{}
	for _, f := range facets {
		m[f.Key+"="+f.Value] = f.Count
	}
	return m
}

func TestFacetCounts_AllFiveFacets(t *testing.T) {
	s := openMem(t)
	facetFixture(t, s)

	facets, err := s.FacetCounts("token", SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)

	want := map[string]int{
		"kind=guide":         1,
		"kind=reference":     1,
		"role_tag=documentation": 1,
		"role_tag=reference":     1,
		"content_tag=deploy":   1,
		"content_tag=rollback":   1,
		"doc_type=runbook":     1,
		"origin=described":     1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("facet %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// TestFacetCounts_FullSetNotPage proves the counts are over the whole
// matching set, not just a truncated page: with limit 1 the search returns
// one hit, but the facet counts cover all three documents.
func TestFacetCounts_FullSetNotPage(t *testing.T) {
	s := openMem(t)
	facetFixture(t, s)

	// A limit-1 search returns only one document.
	hits, err := s.SearchFiltered("token", SearchFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("search returned %d hits, want 1", len(hits))
	}

	// Facet counts cover all three documents.
	facets, err := s.FacetCounts("token", SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)
	// kind=guide: 1 doc, kind=reference: 1 doc, c.md has no kind.
	if got["kind=guide"] != 1 || got["kind=reference"] != 1 {
		t.Errorf("kind counts wrong: %v", got)
	}
	// content_tag: both deploy and rollback are present, across all 3 docs.
	if got["content_tag=deploy"] != 1 || got["content_tag=rollback"] != 1 {
		t.Errorf("content_tag counts wrong: %v", got)
	}
}

// TestFacetCounts_UnderFilter proves the SearchFilter scopes the count set:
// a filter for tag "deploy" keeps only a.md, so role_tag counts reflect a.md
// alone, not the whole corpus.
func TestFacetCounts_UnderFilter(t *testing.T) {
	s := openMem(t)
	facetFixture(t, s)

	facets, err := s.FacetCounts("token", SearchFilter{Tags: []string{"deploy"}})
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)

	// Only a.md carries the "deploy" tag, so it is the only doc counted.
	if got["kind=guide"] != 1 {
		t.Errorf("kind=guide = %d, want 1", got["kind=guide"])
	}
	if got["kind=reference"] != 0 {
		t.Errorf("kind=reference = %d, want 0 (b.md excluded by filter)", got["kind=reference"])
	}
	if got["content_tag=deploy"] != 1 {
		t.Errorf("content_tag=deploy = %d, want 1", got["content_tag=deploy"])
	}
	if got["content_tag=rollback"] != 0 {
		t.Errorf("content_tag=rollback = %d, want 0 (b.md excluded)", got["content_tag=rollback"])
	}
}

// TestFacetCounts_EmptyQuery returns nil for a query with no FTS match.
func TestFacetCounts_EmptyQuery(t *testing.T) {
	s := openMem(t)
	facetFixture(t, s)

	facets, err := s.FacetCounts("", SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if facets != nil {
		t.Errorf("FacetCounts(\"\") = %v, want nil", facets)
	}
}

// TestFacetCounts_NoMatchingDocs returns empty (no facets) when no document
// matches the query.
func TestFacetCounts_NoMatchingDocs(t *testing.T) {
	s := openMem(t)
	facetFixture(t, s)

	facets, err := s.FacetCounts("nonexistentterm", SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(facets) != 0 {
		t.Errorf("FacetCounts(nonexistent) = %v, want empty", facets)
	}
}

// TestFacetCounts_PathPrefix proves the path prefix scopes the count set.
func TestFacetCounts_PathPrefix(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "sub/a.md", Title: "A", Fragments: []Fragment{
		{Text: "token one"},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "other/b.md", Title: "B", Fragments: []Fragment{
		{Text: "token two"},
	}}))
	must(t, s.SetDocumentIdentity(ctx, "sub/a.md", DocIdentity{Kind: "guide"}))
	must(t, s.SetDocumentIdentity(ctx, "other/b.md", DocIdentity{Kind: "reference"}))

	facets, err := s.FacetCounts("token", SearchFilter{PathPrefix: "sub/"})
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)
	if got["kind=guide"] != 1 {
		t.Errorf("kind=guide = %d, want 1", got["kind=guide"])
	}
	if got["kind=reference"] != 0 {
		t.Errorf("kind=reference = %d, want 0 (other/b.md excluded by path)", got["kind=reference"])
	}
}

// TestFacetCounts_MultipleTagsPerDoc proves the recursive CTE explodes a
// comma-joined tag column: a document with two role tags contributes one to
// each tag's count, not one to a combined string.
func TestFacetCounts_MultipleTagsPerDoc(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "multi.md", Title: "M", Fragments: []Fragment{
		{Text: "token"},
	}}))
	must(t, s.SetDocumentIdentity(ctx, "multi.md", DocIdentity{
		RoleTags:    []string{"documentation", "reference"},
		ContentTags: []string{"alpha", "beta", "gamma"},
	}))

	facets, err := s.FacetCounts("token", SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)

	if got["role_tag=documentation"] != 1 {
		t.Errorf("role_tag=documentation = %d, want 1", got["role_tag=documentation"])
	}
	if got["role_tag=reference"] != 1 {
		t.Errorf("role_tag=reference = %d, want 1", got["role_tag=reference"])
	}
	if got["content_tag=alpha"] != 1 || got["content_tag=beta"] != 1 || got["content_tag=gamma"] != 1 {
		t.Errorf("content_tag counts wrong: %v", got)
	}
	// No spurious combined-string entry.
	if _, ok := got["role_tag=documentation,reference"]; ok {
		t.Error("combined role_tag string appeared — CTE did not explode correctly")
	}
}

// TestFacetCounts_MultiDocSharedTag proves a tag shared by two documents
// counts as 2, not 1.
func TestFacetCounts_MultiDocSharedTag(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "x.md", Title: "X", Fragments: []Fragment{{Text: "token"}}}))
	must(t, s.Ingest(ctx, Document{Path: "y.md", Title: "Y", Fragments: []Fragment{{Text: "token"}}}))
	must(t, s.SetDocumentIdentity(ctx, "x.md", DocIdentity{RoleTags: []string{"reference"}}))
	must(t, s.SetDocumentIdentity(ctx, "y.md", DocIdentity{RoleTags: []string{"reference"}}))

	facets, err := s.FacetCounts("token", SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)
	if got["role_tag=reference"] != 2 {
		t.Errorf("role_tag=reference = %d, want 2", got["role_tag=reference"])
	}
}

// vecPoolFixture builds five documents, every fragment containing the token
// "token" so the FULL FTS set for the query "token" is all five. Four of them
// are pure-"token" (fake embedder vector [1,0,0], cosine 1.0 against the
// "token" query) and one — e.md — is mixed ("token deploy billing", vector
// [1,1,1], cosine 1/sqrt(3) ≈ 0.577). In vec mode the candidate pool is the
// limit*4 best vector hits, so at limit 1 the pool is the four pure docs and
// e.md is excluded. The facet identity is arranged so the pool and the full
// set disagree on several values:
//
//	path    kind         role_tag        content_tag  doc_type
//	a.md    guide        documentation   deploy       runbook
//	b.md    reference    reference
//	c.md    guide                   deploy
//	d.md                                       manual
//	e.md    guide                   rollback     manual   (out of the pool)
//
// so the full FTS set would report kind=guide 3, content_tag=rollback 1,
// doc_type=manual 2, while the pool reports kind=guide 2, content_tag=rollback
// 0, doc_type=manual 1. Those are exactly the numbers a pool-scoped count must
// produce and a full-set count would not.
func vecPoolFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	doc := func(path, text, kind, role, content, doctype string) {
		t.Helper()
		must(t, s.Ingest(ctx, Document{Path: path, Title: path, Fragments: []Fragment{{Text: text}}}))
		must(t, s.SetDocumentIdentity(ctx, path, DocIdentity{
			Kind: kind, RoleTags: idents(role), ContentTags: idents(content), DocType: doctype,
		}))
	}
	doc("a.md", "token", "guide", "documentation", "deploy", "runbook")
	doc("b.md", "token", "reference", "reference", "", "")
	doc("c.md", "token", "guide", "", "deploy", "")
	doc("d.md", "token", "", "", "", "manual")
	doc("e.md", "token deploy billing", "guide", "", "rollback", "manual")
	// origin lives on fragments: mark a.md's fragment so the origin facet has a
	// value inside the pool.
	if _, err := s.db.Exec(`UPDATE fragments SET origin='described' WHERE doc_id=(SELECT id FROM documents WHERE path='a.md') AND page=0`); err != nil {
		t.Fatal(err)
	}
}

func idents(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}

// TestFacetCounts_VecPoolNotFullSet proves the vec-mode facet counts are
// scoped to the candidate pool the vec search would rank, not the full FTS set.
// All five fixture documents match "token" textually, so a full-set (bm25)
// count would report kind=guide 3, content_tag=rollback 1, doc_type=manual 2;
// the vec pool at limit 1 holds only the four pure docs and must report
// kind=guide 2, content_tag=rollback 0, doc_type=manual 1. A count that walks
// the whole matching set fails exactly those assertions.
func TestFacetCounts_VecPoolNotFullSet(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	vecPoolFixture(t, s)
	ctx := context.Background()

	// Sanity: the full FTS set really is five, so the pool is a proper subset.
	all, err := s.searchLocal("token", SearchFilter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("FTS matched %d fragments, want 5 — fixture premise is off", len(all))
	}

	facets, err := s.FacetCountsMode(ctx, "token", SearchFilter{}, "vec", 1)
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)

	// Pool values (the four pure docs), not the full-set values.
	if got["kind=guide"] != 2 {
		t.Errorf("kind=guide = %d, want 2 (full-set count 3 would mean the pool was not applied)", got["kind=guide"])
	}
	if got["kind=reference"] != 1 {
		t.Errorf("kind=reference = %d, want 1", got["kind=reference"])
	}
	// e.md carries rollback; it is out of the pool, so it must not count.
	if got["content_tag=rollback"] != 0 {
		t.Errorf("content_tag=rollback = %d, want 0 (e.md is out of the vec pool)", got["content_tag=rollback"])
	}
	if got["content_tag=deploy"] != 2 {
		t.Errorf("content_tag=deploy = %d, want 2 (a.md and c.md are both in the pool)", got["content_tag=deploy"])
	}
	// doc_type manual: d.md in the pool, e.md out → 1, not the full-set 2.
	if got["doc_type=manual"] != 1 {
		t.Errorf("doc_type=manual = %d, want 1 (full-set count 2 would mean the pool was not applied)", got["doc_type=manual"])
	}
	if got["doc_type=runbook"] != 1 {
		t.Errorf("doc_type=runbook = %d, want 1", got["doc_type=runbook"])
	}
	// The CTE tag path under a non-nil pool must also be scoped: e.md's
	// rollback role_tag is absent.
	if got["role_tag=documentation"] != 1 || got["role_tag=reference"] != 1 {
		t.Errorf("role_tag counts wrong under pool: %v", got)
	}
	// origin: a.md is in the pool, so its described fragment counts once.
	if got["origin=described"] != 1 {
		t.Errorf("origin=described = %d, want 1", got["origin=described"])
	}
}

// TestFacetCounts_VecZeroCandidatesEmptyPool proves the zero-candidate vec case:
// a store whose fragments have no vectors yields an EMPTY (non-nil) candidate
// pool, which scopes the count to nothing — all facets zero — not to the whole
// FTS set. If the empty pool were treated as nil (no scoping), the full FTS-set
// counts would leak through and these would be non-zero. The fixture is ingested
// WITHOUT an embedder (so no fragment_vectors rows are written), then an
// embedder is set and a vec facet count is taken: every document matches the
// query textually, so a full-set count would be non-zero on several facets.
func TestFacetCounts_VecZeroCandidatesEmptyPool(t *testing.T) {
	s := openMem(t)
	// Ingest before any embedder: fragments are stored with no vectors.
	vecPoolFixture(t, s)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))

	// The FTS set is non-empty, so this is not a no-match case: a full-set
	// count would report kind=guide 3, content_tag=deploy 2, etc.
	all, err := s.searchLocal("token", SearchFilter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("FTS matched nothing — the fixture is not set up")
	}

	facets, err := s.FacetCountsMode(context.Background(), "token", SearchFilter{}, "vec", 1)
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)
	// No vector candidates → empty pool → every facet zero, not the full set.
	for k, v := range got {
		if v != 0 {
			t.Errorf("facet %s = %d, want 0 (empty vec pool must scope to nothing, not the full set)", k, v)
		}
	}
}

// TestFacetCounts_VecOwnLayer proves FacetCountsOwnLayer applies the same
// candidate-pool scoping as FacetCountsMode for a top-level store (parent nil),
// including the CTE tag path under a non-nil pool: e.md's rollback content_tag
// is out of the pool and must not count.
func TestFacetCounts_VecOwnLayer(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	vecPoolFixture(t, s)
	ctx := context.Background()

	facets, err := s.FacetCountsOwnLayer(ctx, "token", "vec", "", SearchFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)
	if got["kind=guide"] != 2 {
		t.Errorf("kind=guide = %d, want 2 (pool, not the full-set 3)", got["kind=guide"])
	}
	if got["content_tag=rollback"] != 0 {
		t.Errorf("content_tag=rollback = %d, want 0 (e.md out of pool, CTE tag path)", got["content_tag=rollback"])
	}
	if got["content_tag=deploy"] != 2 {
		t.Errorf("content_tag=deploy = %d, want 2 (CTE tag path under non-nil pool)", got["content_tag=deploy"])
	}
	if got["origin=described"] != 1 {
		t.Errorf("origin=described = %d, want 1", got["origin=described"])
	}
}

// TestFacetCounts_HybridPoolProvesHybridMergesSides proves hybrid mode counts
// the union of the bm25 pool and the vec pool. In the fixture every doc matches
// "token" textually, so the bm25 pool (limit*4) and the vec pool are the same
// four pure docs and e.md is excluded from both — the hybrid count must match
// the vec count above, and must not include e.md's rollback/manual.
func TestFacetCounts_HybridPool(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	vecPoolFixture(t, s)
	ctx := context.Background()

	facets, err := s.FacetCountsMode(ctx, "token", SearchFilter{}, "hybrid", 1)
	if err != nil {
		t.Fatal(err)
	}
	got := facetByKeyVal(facets)
	if got["kind=guide"] != 2 {
		t.Errorf("kind=guide = %d, want 2 (hybrid pool excludes e.md)", got["kind=guide"])
	}
	if got["content_tag=rollback"] != 0 {
		t.Errorf("content_tag=rollback = %d, want 0 (e.md out of the hybrid pool)", got["content_tag=rollback"])
	}
	if got["doc_type=manual"] != 1 {
		t.Errorf("doc_type=manual = %d, want 1", got["doc_type=manual"])
	}
	if got["origin=described"] != 1 {
		t.Errorf("origin=described = %d, want 1", got["origin=described"])
	}
}

// TestFacetCounts_VecRequiresEmbedder proves vec mode without an embedder is a
// clean error, not a silent fall-back to the full FTS set (which would report
// the full-set counts and hide the missing embedder).
func TestFacetCounts_VecRequiresEmbedder(t *testing.T) {
	s := openMem(t)
	vecPoolFixture(t, s)
	if _, err := s.FacetCountsMode(context.Background(), "token", SearchFilter{}, "vec", 1); err == nil {
		t.Fatal("FacetCountsMode in vec mode without an embedder should error")
	}
}

// TestFacetCounts_OverlayShadowsParent proves the branch overlay subtracts a
// shadowed parent document's single-valued facet contribution. Parent has a.md
// (kind "guide", doc_type "runbook") and b.md (kind "reference", doc_type
// "manual"); the branch shadows a.md with a copy of kind "reference" and
// doc_type "manual". Over the branch, a.md's parent values must be removed and
// its branch values counted once, so "guide" and "runbook" vanish.
//
// This guards the arg order of shadowedFacetValues' single-value (kind/doc_type)
// query: it binds baseWhere's placeholders in the order [match, pargs..., in...].
// With the order reversed, a parent path string lands on the FTS MATCH
// placeholder, the sub-query errors or comes back empty, and the parent's
// shadowed contribution is never subtracted — "guide"/"runbook" survive as a
// phantom count.
func TestFacetCounts_OverlayShadowsParent(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx := context.Background()

	main, _ := reg.Get("main")
	must(t, main.Ingest(ctx, Document{Path: "file:///a.md", Title: "A", Fragments: []Fragment{{Text: "token"}}}))
	must(t, main.Ingest(ctx, Document{Path: "file:///b.md", Title: "B", Fragments: []Fragment{{Text: "token"}}}))
	must(t, main.SetDocumentIdentity(ctx, "file:///a.md", DocIdentity{Kind: "guide", DocType: "runbook"}))
	must(t, main.SetDocumentIdentity(ctx, "file:///b.md", DocIdentity{Kind: "reference", DocType: "manual"}))

	if err := reg.ForkBranch("feat", "main"); err != nil {
		t.Fatal(err)
	}
	feat, _ := reg.Get("feat")
	if feat.parent == nil {
		t.Fatal("branch not wired to its parent")
	}
	// Shadow the parent's a.md with a branch copy carrying different identity.
	must(t, feat.Ingest(ctx, Document{Path: "file:///a.md", Title: "A2", Fragments: []Fragment{{Text: "token"}}}))
	must(t, feat.SetDocumentIdentity(ctx, "file:///a.md", DocIdentity{Kind: "reference", DocType: "manual"}))

	facets, err := feat.FacetCounts("token", SearchFilter{})
	if err != nil {
		t.Fatalf("FacetCounts: %v", err)
	}
	got := facetByKeyVal(facets)

	// a.md's branch copy (reference/manual) plus b.md (reference/manual):
	// reference and manual each count 2.
	if got["kind=reference"] != 2 {
		t.Errorf("kind=reference = %d, want 2 (all: %v)", got["kind=reference"], got)
	}
	if got["doc_type=manual"] != 2 {
		t.Errorf("doc_type=manual = %d, want 2 (all: %v)", got["doc_type=manual"], got)
	}
	// a.md's parent values (guide/runbook) are shadowed and must be subtracted.
	if got["kind=guide"] != 0 {
		t.Errorf("kind=guide = %d, want 0 (shadowed parent a.md over-counted: %v)", got["kind=guide"], got)
	}
	if got["doc_type=runbook"] != 0 {
		t.Errorf("doc_type=runbook = %d, want 0 (shadowed parent a.md over-counted: %v)", got["doc_type=runbook"], got)
	}
}
