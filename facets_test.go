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
