package raglit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Session branches ("idx@session", USER 2026-10-30): a session never writes
// main; it writes a branch of it that exists from its first write, reads as
// its parent until then, and is removed 7 days after its last write.

func TestNormalizeIndexName_KeepsOneSessionSeparator(t *testing.T) {
	for in, want := range map[string]string{
		"proj__code@s-1A": "proj__code@s-1a",
		"@code":           "code",
		"code@a@b":        "code@ab",
		"code@":           "code",
		"../x@y":          "x@y",
	} {
		if got := normalizeIndexName(in); got != want {
			t.Errorf("normalizeIndexName(%q) = %q, want %q", in, got, want)
		}
	}
	if p, ok := SessionBranchParent("proj__code@s1"); !ok || p != "proj__code" {
		t.Errorf("SessionBranchParent = %q, %v", p, ok)
	}
	if _, ok := SessionBranchParent("proj__code"); ok {
		t.Error("a plain index is not a session branch")
	}
}

func TestSessionBranch_ExistsFromItsFirstWrite(t *testing.T) {
	reg := aclRegistry(t)
	ctx := context.Background()
	main, err := reg.Write(as("aw4"), "p__code")
	if err != nil {
		t.Fatal(err)
	}
	must(t, main.Ingest(ctx, Document{Path: "file:///main.go", Title: "M", Fragments: []Fragment{{Text: "walrus main"}}}))
	must(t, reg.SetVisibility(as("aw4"), "p__code", Public))

	// Before any write: no branch, and reading it reads main. Nothing created.
	st, err := reg.Read(as("key#s1"), "p__code@s1")
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := st.Search("walrus", 5); len(h) != 1 {
		t.Fatalf("unwritten branch: %d hits, want main's 1", len(h))
	}
	if reg.exists("p__code@s1") {
		t.Fatal("a read created the session branch")
	}
	if got := reg.Readable(as("key#s1"), []string{"p__code@s1"}); len(got) != 1 || got[0] != "p__code" {
		t.Fatalf("Readable(unwritten branch) = %v, want [p__code]", got)
	}

	// The first write forks it, owned by the writer, private.
	br, err := reg.Write(as("key#s1"), "p__code@s1")
	if err != nil {
		t.Fatal(err)
	}
	must(t, br.Ingest(ctx, Document{Path: "file:///note.md", Title: "N", Fragments: []Fragment{{Text: "walrus note"}}}))
	if a := reg.ACLOf("p__code@s1"); a.Owner != "key#s1" || a.Visibility != Private {
		t.Fatalf("branch ACL = %+v, want owner key#s1, private", a)
	}
	st, _ = reg.Read(as("key#s1"), "p__code@s1")
	if h, _ := st.Search("walrus", 5); len(h) != 2 {
		t.Fatalf("branch over main: %d hits, want 2", len(h))
	}
	// Main is untouched, and another session cannot see the branch.
	st, _ = reg.Read(as("aw4"), "p__code")
	if h, _ := st.Search("walrus", 5); len(h) != 1 {
		t.Fatalf("main after a branch write: %d hits, want 1", len(h))
	}
	if _, err := reg.Read(as("key#s2"), "p__code@s1"); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("another session reading the branch: %v, want ErrNoIndex", err)
	}
}

// A sessions index has no main of its own (sessions never write main): the
// first branch write creates the parent empty, admin-owned and public.
func TestSessionBranch_CreatesAPublicEmptyParent(t *testing.T) {
	reg := aclRegistry(t)
	if _, err := reg.Write(as("key#s1"), "p__sessions@s1"); err != nil {
		t.Fatal(err)
	}
	if a := reg.ACLOf("p__sessions"); a.Owner != "admin" || a.Visibility != Public {
		t.Fatalf("parent ACL = %+v, want admin, public", a)
	}
	if _, err := reg.Write(as("key#s1"), "p__sessions"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a session writing main: %v, want ErrForbidden", err)
	}
}

func TestReapSessionBranches_RemovesOnlyIdleSessionBranches(t *testing.T) {
	reg := aclRegistry(t)
	for _, n := range []string{"p__code@old", "p__code@new"} {
		if _, err := reg.Write(as("key#"+n), n); err != nil {
			t.Fatal(err)
		}
	}
	must(t, reg.ForkBranchAs(as("admin"), "p__code-wt1", "p__code"))
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour)
	for _, n := range []string{"p__code@old", "p__code-wt1"} {
		ents, _ := os.ReadDir(string(reg.indexHome(n)))
		for _, e := range ents {
			must(t, os.Chtimes(filepath.Join(string(reg.indexHome(n)), e.Name()), old, old))
		}
	}
	reaped, err := reg.ReapSessionBranches(0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0] != "p__code@old" {
		t.Fatalf("reaped %v, want [p__code@old]", reaped)
	}
	if reg.exists("p__code@old") || !reg.exists("p__code@new") || !reg.exists("p__code-wt1") {
		t.Fatal("reaper removed the wrong branches")
	}
}

// A session's default read (the indexes scoped to its directory) includes its
// own branch of each, and not another session's.
func TestScopedFor_IncludesTheCallersOwnSessionBranches(t *testing.T) {
	reg := aclRegistry(t)
	main, err := reg.Write(as("aw4"), "p__code")
	if err != nil {
		t.Fatal(err)
	}
	must(t, reg.SetVisibility(as("aw4"), "p__code", Public))
	if _, err := main.SetScopes([]string{"/work/p"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"s1", "s2"} {
		if _, err := reg.Write(as("key#"+s), "p__code@"+s); err != nil {
			t.Fatal(err)
		}
	}
	got := reg.ScopedFor(as("key#s1"), "/work/p/src")
	if len(got) != 2 || got[0] != "p__code" || got[1] != "p__code@s1" {
		t.Fatalf("ScopedFor = %v, want [p__code p__code@s1]", got)
	}
}

// A session's folded history sits in a branch of an unscoped sessions index;
// its default read includes it because the session's key owns it.
func TestScopedFor_IncludesTheCallersSessionsBranch(t *testing.T) {
	reg := aclRegistry(t)
	if _, err := reg.Write(as("key#s1"), "p__sessions@s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Write(as("key#s2"), "p__sessions@s2"); err != nil {
		t.Fatal(err)
	}
	if got := reg.ScopedFor(as("key#s1"), "/anywhere"); len(got) != 1 || got[0] != "p__sessions@s1" {
		t.Fatalf("ScopedFor = %v, want [p__sessions@s1]", got)
	}
	if got := reg.ScopedFor(as("admin"), "/anywhere"); len(got) != 0 {
		t.Fatalf("admin's default read = %v, want none of the sessions' branches", got)
	}
}
