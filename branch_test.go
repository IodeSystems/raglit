package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBranch_OverlayCOWTombstoneAndLifecycle(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx := context.Background()

	// Parent "main" with two docs (distinctive tokens for FTS isolation).
	main, _ := reg.Get("main")
	must(t, main.Ingest(ctx, Document{Path: "file:///a.md", Title: "A", Fragments: []Fragment{{Text: "apple zebra"}}}))
	must(t, main.Ingest(ctx, Document{Path: "file:///b.md", Title: "B", Fragments: []Fragment{{Text: "banana walrus"}}}))

	// Fork a branch off main.
	if err := reg.ForkBranch("feat", "main"); err != nil {
		t.Fatal(err)
	}
	feat, _ := reg.Get("feat")
	if feat.parent == nil {
		t.Fatal("branch not wired to its parent")
	}

	// A fresh branch overlays the parent: it sees both parent docs.
	if h, _ := feat.Search("zebra", 5); len(h) != 1 {
		t.Fatalf("branch should see parent doc a: %d hits", len(h))
	}
	if docs, _ := feat.Documents(); len(docs) != 2 {
		t.Fatalf("branch should list both parent docs: %d", len(docs))
	}

	// Copy-on-write: modify a.md IN the branch → it shadows the parent's a.md.
	must(t, feat.Ingest(ctx, Document{Path: "file:///a.md", Title: "A2", Fragments: []Fragment{{Text: "apricot phoenix"}}}))
	if h, _ := feat.Search("zebra", 5); len(h) != 0 {
		t.Fatalf("branch a shadows parent a — 'zebra' should be gone: %d", len(h))
	}
	if h, _ := feat.Search("phoenix", 5); len(h) != 1 {
		t.Fatalf("branch a's own content should be searchable: %d", len(h))
	}
	// Parent is untouched (COW).
	if h, _ := main.Search("zebra", 5); len(h) != 1 {
		t.Fatalf("parent must keep its a.md: %d", len(h))
	}
	if h, _ := main.Search("phoenix", 5); len(h) != 0 {
		t.Fatalf("parent must not see branch edits: %d", len(h))
	}
	// get_document overlay resolves the branch's version.
	if c, _ := feat.DocText("file:///a.md", 0, 0, 0); c.Title != "A2" {
		t.Fatalf("DocText should return the branch's a.md (A2), got %q", c.Title)
	}

	// Delete b.md in the branch (tombstone): hidden in branch, present in parent.
	must(t, feat.DeleteDocument("file:///b.md"))
	if h, _ := feat.Search("walrus", 5); len(h) != 0 {
		t.Fatalf("tombstoned b must be hidden in the branch: %d", len(h))
	}
	if h, _ := main.Search("walrus", 5); len(h) != 1 {
		t.Fatalf("parent b must survive the branch tombstone: %d", len(h))
	}
	if _, err := feat.DocText("file:///b.md", 0, 0, 0); err == nil {
		t.Fatal("tombstoned b should be not-found in the branch")
	}
	if docs, _ := feat.Documents(); len(docs) != 1 {
		t.Fatalf("branch should now list only a.md: %d docs", len(docs))
	}

	// list branches: lineage + age + local diff count (a's branch copy = 1).
	brs, err := reg.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(brs) != 1 || brs[0].Name != "feat" || brs[0].Parent != "main" {
		t.Fatalf("ListBranches = %+v", brs)
	}
	if brs[0].CreatedAt == 0 || brs[0].LastAccessedAt == 0 {
		t.Fatalf("branch age/last-access unset: %+v", brs[0])
	}
	if brs[0].Documents != 1 {
		t.Fatalf("branch local docs = %d, want 1 (only a.md's branch copy)", brs[0].Documents)
	}

	// delete branch: storage removed, parent intact.
	if err := reg.DeleteBranch("feat"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "indexes", "feat")); !os.IsNotExist(err) {
		t.Fatal("branch storage was not removed")
	}
	if h, _ := main.Search("walrus", 5); len(h) != 1 {
		t.Fatal("parent must be intact after branch delete")
	}
	if brs, _ := reg.ListBranches(); len(brs) != 0 {
		t.Fatalf("no branches should remain: %+v", brs)
	}
}
