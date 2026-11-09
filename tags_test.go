package raglit

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func wantTags(t *testing.T, s *Store, path string, want ...string) {
	t.Helper()
	got, err := s.Tags(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tags(%s) = %q, want %q", path, got, want)
	}
}

func filteredPaths(t *testing.T, s *Store, query string, tags ...string) []string {
	t.Helper()
	h, err := s.SearchFiltered(query, SearchFilter{Tags: tags}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return hitPaths(h)
}

// A caller's tag is matched whole, after the same normalization the model's
// tags get, and none of the model-tag rules apply: one tag is enough, and a
// tag past the model's word and length limits is kept.
func TestSetTags_WholeTagNoModelRules(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "s.md", Fragments: []Fragment{{Text: "walrus plan"}}}))
	long := "a caller tag that is well past the forty characters and three words a model may write"
	must(t, s.SetTags("s.md", []string{" Kind:Strategy ", "kind:strategy", "", long, "a, b"}))
	wantTags(t, s, "s.md", "kind:strategy", long, "a b")

	for _, c := range []struct {
		tag  string
		want int
	}{
		{"kind:strategy", 1},
		{"KIND:STRATEGY", 1},
		{"kind:strategyx", 0},
		{"kind", 0},
		{"strategy", 0},
		{long, 1},
		{"a b", 1},
	} {
		if got := filteredPaths(t, s, "walrus", c.tag); len(got) != c.want {
			t.Errorf("filter %q = %v, want %d hit(s)", c.tag, got, c.want)
		}
	}
}

// SetTags on a committed document changes what a filter keeps, and nothing
// else: no fragment, no FTS hit for the tag's text, no identity job.
func TestSetTags_CommittedDocumentNoReingest(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "m.md", Fragments: []Fragment{{Text: "walrus memory"}}}))
	s.SetIdentifier(NewIdentifier(&countingChatter{}, "m"))
	frags := countRows(t, s, `SELECT COUNT(*) FROM fragments`)
	jobs := countRows(t, s, `SELECT COUNT(*) FROM identity_jobs`)

	if got := filteredPaths(t, s, "walrus", "kind:memory"); len(got) != 0 {
		t.Fatalf("untagged document matched: %v", got)
	}
	must(t, s.SetTags("m.md", []string{"kind:memory"}))
	if got := filteredPaths(t, s, "walrus", "kind:memory"); len(got) != 1 {
		t.Fatalf("tagged document not kept by the filter: %v", got)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM fragments`); n != frags {
		t.Fatalf("fragments %d → %d: a tag wrote a fragment", frags, n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM identity_jobs`); n != jobs {
		t.Fatalf("identity jobs %d → %d: a tag queued a caption", jobs, n)
	}
	if h, _ := s.Search("memory", 10); len(h) != 1 {
		t.Fatalf("FTS hits for the text = %d, want 1 (the fragment, not the tag)", len(h))
	}
	if h, _ := s.Search("kind", 10); len(h) != 0 {
		t.Fatalf("the tag's own text is an FTS hit: %v", hitPaths(h))
	}

	// Replace, then clear.
	must(t, s.SetTags("m.md", []string{"kind:note"}))
	if got := filteredPaths(t, s, "walrus", "kind:memory"); len(got) != 0 {
		t.Fatalf("a replaced tag still matches: %v", got)
	}
	wantTags(t, s, "m.md", "kind:note")
	must(t, s.SetTags("m.md", nil))
	wantTags(t, s, "m.md")
	if got := filteredPaths(t, s, "walrus", "kind:note"); len(got) != 0 {
		t.Fatalf("a cleared tag still matches: %v", got)
	}
}

// Tags set before a path has a document wait for its first commit; the last
// SetTags before that commit wins, and an empty one cancels what was held.
func TestSetTags_PendingAppliedAtCommit(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.SetTags("p.md", []string{"first"}))
	must(t, s.SetTags("p.md", []string{"second", "kind:x"}))
	if _, err := s.Tags("p.md"); err == nil {
		t.Fatal("Tags on a path with no document: want an error")
	}
	must(t, s.Ingest(ctx, Document{Path: "p.md", Fragments: []Fragment{{Text: "walrus"}}}))
	wantTags(t, s, "p.md", "second", "kind:x")
	if n := countRows(t, s, `SELECT COUNT(*) FROM pending_tags`); n != 0 {
		t.Fatalf("%d pending row(s) left after the commit", n)
	}

	must(t, s.SetTags("q.md", []string{"held"}))
	must(t, s.SetTags("q.md", []string{}))
	must(t, s.Ingest(ctx, Document{Path: "q.md", Fragments: []Fragment{{Text: "walrus"}}}))
	wantTags(t, s, "q.md")
}

// The queued path: enqueue, tag, then the worker's commit (commitDoc) applies
// the tags. A re-ingest of changed bytes keeps them, and a caption the model
// writes afterwards does not touch them.
func TestSetTags_WorkerCommitReingestAndIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := OpenHome(Home(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ingest := func(text string, tags []string) string {
		t.Helper()
		p, err := st.SaveUpload("mem/a.md", []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		url := UploadURL(p)
		if _, err := st.Enqueue(url, ""); err != nil {
			t.Fatal(err)
		}
		if tags != nil {
			must(t, st.SetTags(url, tags))
		}
		w := &Worker{Store: st}
		for range 5 {
			if did, err := w.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			} else if !did {
				break
			}
		}
		return url
	}
	url := ingest(psaText+" walrus first version", []string{"kind:memory"})
	wantTags(t, st, url, "kind:memory")

	url = ingest(psaText+" walrus second version", nil)
	if h, _ := st.Search("second", 5); len(h) != 1 {
		t.Fatalf("the re-ingest did not commit: %d hits", len(h))
	}
	wantTags(t, st, url, "kind:memory")

	st.SetIdentifier(NewIdentifier(&identityChatter{replies: []string{
		`{"name":"A memory note, captioned","summary":"A note held in this index, summarised for this test at sufficient length.","kind":"docs:notes","content_tags":["purchase agreement","parcel conveyance","closing terms"],"role_tags":["reference"]}`,
	}}, "m"))
	if _, err := st.EnqueueIdentity(url, true); err != nil {
		t.Fatal(err)
	}
	if n, err := (&IdentityWorker{Store: st, Slots: 1}).Drain(ctx); err != nil || n != 1 {
		t.Fatalf("identity drain = %d, %v", n, err)
	}
	id, err := st.DocumentIdentity(url)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.ContentTags) == 0 {
		t.Fatalf("the model wrote no content tags: %+v", id)
	}
	wantTags(t, st, url, "kind:memory")
	// The filter now holds both kinds of tag, ANDed.
	if got := filteredPaths(t, st, "walrus", "kind:memory", id.ContentTags[0]); len(got) != 1 {
		t.Fatalf("caller AND content tag = %v, want the document", got)
	}

	// A person's correction rewrites the identity columns, not the caller's.
	must(t, st.SetDocumentIdentity(ctx, url, DocIdentity{ContentTags: []string{"other"}, RoleTags: []string{"reference"}}))
	wantTags(t, st, url, "kind:memory")
}

// RemoveDocument drops the document's tags with its row, and tags still held
// for a path that never committed.
func TestSetTags_RemoveDropsTagsAndPending(t *testing.T) {
	ctx := context.Background()
	st, err := OpenHome(Home(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must(t, st.Ingest(ctx, Document{Path: "r.md", Fragments: []Fragment{{Text: "walrus"}}}))
	must(t, st.SetTags("r.md", []string{"kind:x"}))
	must(t, st.SetTags("ghost.md", []string{"kind:x"}))
	must(t, st.RemoveDocument("r.md", false))
	must(t, st.RemoveDocument("ghost.md", false))
	if n := countRows(t, st, `SELECT COUNT(*) FROM pending_tags`); n != 0 {
		t.Fatalf("%d pending row(s) survived the remove", n)
	}
	// A new document at the same path starts untagged.
	must(t, st.Ingest(ctx, Document{Path: "r.md", Fragments: []Fragment{{Text: "walrus"}}}))
	must(t, st.Ingest(ctx, Document{Path: "ghost.md", Fragments: []Fragment{{Text: "walrus"}}}))
	wantTags(t, st, "r.md")
	wantTags(t, st, "ghost.md")
}

// On a branch, SetTags works on the branch's own documents and fails loudly on
// one only the parent holds (copy-up is deferred).
func TestSetTags_BranchParentOnlyDocumentFails(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx := context.Background()
	main, _ := reg.Get("main")
	must(t, main.Ingest(ctx, Document{Path: "file:///a.md", Fragments: []Fragment{{Text: "apple"}}}))
	must(t, reg.ForkBranch("feat", "main"))
	feat, _ := reg.Get("feat")

	err = feat.SetTags("file:///a.md", []string{"kind:x"})
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("SetTags on a parent-only document = %v, want a loud error", err)
	}
	if n := countRows(t, feat, `SELECT COUNT(*) FROM pending_tags`); n != 0 {
		t.Fatalf("the refused tags were held: %d row(s)", n)
	}

	// The branch's own copy takes tags; the parent's does not change.
	must(t, feat.Ingest(ctx, Document{Path: "file:///a.md", Fragments: []Fragment{{Text: "apricot"}}}))
	must(t, feat.SetTags("file:///a.md", []string{"kind:x"}))
	wantTags(t, feat, "file:///a.md", "kind:x")
	wantTags(t, main, "file:///a.md")

	// A path neither layer has is held, as on any index.
	must(t, feat.SetTags("file:///new.md", []string{"kind:y"}))
	must(t, feat.Ingest(ctx, Document{Path: "file:///new.md", Fragments: []Fragment{{Text: "walrus"}}}))
	wantTags(t, feat, "file:///new.md", "kind:y")
}

// An index created before documents.tags opens, gains the column and the
// pending table, and its existing document takes tags.
func TestMigrate_OpensAnIndexThatPredatesCallerTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE documents (
		id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE,
		title TEXT NOT NULL DEFAULT '', added_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO documents(path, title, added_at) VALUES('old.md', 'Old', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening an index that predates caller tags: %v", err)
	}
	defer s.Close()
	if has, err := hasColumn(s.db, "documents", "tags"); err != nil || !has {
		t.Fatalf("documents.tags after migrate: %v, %v", has, err)
	}
	wantTags(t, s, "old.md")
	must(t, s.Ingest(context.Background(), Document{Path: "old.md", Fragments: []Fragment{{Text: "walrus"}}}))
	must(t, s.SetTags("old.md", []string{"kind:x"}))
	must(t, s.SetTags("later.md", []string{"kind:y"}))
	if got := filteredPaths(t, s, "walrus", "kind:x"); len(got) != 1 {
		t.Fatalf("filter on a migrated index = %v", got)
	}
}
