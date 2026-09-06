package raglit

import (
	"context"
	"testing"
)

// TestSearchPath_ConstrainsToSubtree covers the path-prefix filter across BM25,
// vector, hybrid, and figure search.
func TestSearchPath_ConstrainsToSubtree(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()

	// Same content under two subtrees; only /src/api/ should survive the filter.
	must(t, s.Ingest(ctx, Document{Path: "/repo/src/api/auth.md", Fragments: []Fragment{
		{Text: "auth token refresh handshake"},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "/repo/src/web/auth.md", Fragments: []Fragment{
		{Text: "auth token refresh handshake"},
	}}))
	must(t, s.Ingest(ctx, Document{Path: "/repo/docs/auth.md", Fragments: []Fragment{
		{Text: "auth token refresh handshake"},
	}}))

	// Unscoped: all three match.
	if h, _ := s.Search("auth token", 10); len(h) != 3 {
		t.Fatalf("unscoped BM25 = %d hits, want 3", len(h))
	}

	// BM25 scoped to /repo/src/api/ → only that document.
	h, err := s.SearchPath("auth token", "/repo/src/api/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 1 || h[0].Path != "/repo/src/api/auth.md" {
		t.Fatalf("scoped BM25 = %+v, want just /repo/src/api/auth.md", h)
	}

	// A prefix one level up scopes to the whole /repo/src/ subtree (2 docs).
	if h, _ := s.SearchPath("auth token", "/repo/src/", 10); len(h) != 2 {
		t.Fatalf("subtree BM25 = %d hits, want 2", len(h))
	}

	// vec + hybrid honor the same scope.
	if h, err := s.VecSearchPath(ctx, "auth token", "/repo/src/api/", 10); err != nil || len(h) != 1 {
		t.Fatalf("scoped vec = %d hits (err %v), want 1", len(h), err)
	}
	if h, err := s.HybridSearchPath(ctx, "auth token", "/repo/docs/", 10); err != nil || len(h) != 1 || h[0].Path != "/repo/docs/auth.md" {
		t.Fatalf("scoped hybrid = %+v (err %v), want just /repo/docs/auth.md", h, err)
	}
}

func TestSearchFiguresPath_ConstrainsToSubtree(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()

	// Two docs in different subtrees, each with a billing figure.
	for _, p := range []string{"/a/one.pdf", "/b/two.pdf"} {
		if _, err := s.db.Exec(`INSERT INTO documents(path,title,added_at) VALUES(?, '', 1)`, p); err != nil {
			t.Fatal(err)
		}
	}
	billing := encodeVec([]float32{0, 0, 1})
	var docIDs []int64
	rows, err := s.db.Query(`SELECT id FROM documents ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		docIDs = append(docIDs, id)
	}
	rows.Close()
	for _, id := range docIDs {
		res, err := s.db.Exec(`INSERT INTO media(doc_id,page,ord,kind,description,image_path) VALUES(?,1,0,'figure','a billing chart','')`, id)
		if err != nil {
			t.Fatal(err)
		}
		mid, _ := res.LastInsertId()
		if _, err := s.db.Exec(`INSERT INTO media_vectors(media_id,dim,vec,space) VALUES(?,3,?,'text')`, mid, billing); err != nil {
			t.Fatal(err)
		}
	}

	if figs, _ := s.SearchFigures(ctx, "billing", 10); len(figs) != 2 {
		t.Fatalf("unscoped figures = %d, want 2", len(figs))
	}
	figs, err := s.SearchFiguresPath(ctx, "billing", "/a/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(figs) != 1 || figs[0].Path != "/a/one.pdf" {
		t.Fatalf("scoped figures = %+v, want just /a/one.pdf", figs)
	}
}
