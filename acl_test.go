package raglit

import (
	"context"
	"errors"
	"testing"
)

func aclRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	if err := reg.EnableACL("admin"); err != nil {
		t.Fatal(err)
	}
	return reg
}

func as(p string) context.Context { return WithPrincipal(context.Background(), p) }

// The case dun needs: a private branch over a public project index.
func TestACL_PrivateBranchOverPublicParent(t *testing.T) {
	reg := aclRegistry(t)
	ctx := context.Background()

	proj, err := reg.Write(as("alice"), "proj")
	if err != nil {
		t.Fatal(err)
	}
	must(t, proj.Ingest(ctx, Document{Path: "file:///shared.md", Title: "S", Fragments: []Fragment{{Text: "shared zebra"}}}))
	must(t, reg.SetVisibility(as("alice"), "proj", Public))

	if err := reg.ForkBranchAs(as("alice"), "proj-alice", "proj"); err != nil {
		t.Fatal(err)
	}
	mine, err := reg.Write(as("alice"), "proj-alice")
	if err != nil {
		t.Fatal(err)
	}
	must(t, mine.Ingest(ctx, Document{Path: "file:///mine.md", Title: "M", Fragments: []Fragment{{Text: "private zebra"}}}))

	// Owner reads both layers through the branch.
	st, err := reg.Read(as("alice"), "proj-alice")
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := st.Search("zebra", 5); len(h) != 2 {
		t.Fatalf("owner through branch: %d hits, want 2", len(h))
	}

	// Another principal: the branch does not exist; the parent shows only itself.
	if _, err := reg.Read(as("bob"), "proj-alice"); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("bob reading alice's private branch: err=%v, want ErrNoIndex", err)
	}
	st, err = reg.Read(as("bob"), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := st.Search("zebra", 5); len(h) != 1 || h[0].Path != "file:///shared.md" {
		t.Fatalf("bob through parent: %+v, want only shared.md", h)
	}

	// Public is for reading: bob cannot write to alice's index.
	if _, err := reg.Write(as("bob"), "proj"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob writing alice's public index: err=%v, want ErrForbidden", err)
	}
	// ...but may fork it, and owns the fork.
	if err := reg.ForkBranchAs(as("bob"), "proj-bob", "proj"); err != nil {
		t.Fatal(err)
	}
	if a := reg.ACLOf("proj-bob"); a.Owner != "bob" || a.Visibility != Private {
		t.Fatalf("bob's fork ACL = %+v", a)
	}

	got := reg.Readable(as("bob"), reg.Names())
	for _, n := range got {
		if n == "proj-alice" {
			t.Fatalf("Readable leaked alice's branch to bob: %v", got)
		}
	}
}

// A public branch over a private parent must not expose the parent.
func TestACL_PublicBranchDoesNotOpenPrivateParent(t *testing.T) {
	reg := aclRegistry(t)
	if _, err := reg.Write(as("alice"), "secret"); err != nil {
		t.Fatal(err)
	}
	must(t, reg.ForkBranchAs(as("alice"), "leak", "secret"))
	must(t, reg.SetVisibility(as("alice"), "leak", Public))
	if reg.CanRead(as("bob"), "leak") {
		t.Fatal("a public branch over a private parent is readable by others")
	}
	if err := reg.ForkBranchAs(as("bob"), "b2", "secret"); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("bob forking alice's private index: err=%v", err)
	}
}

// Indexes from before access control belong to the admin, private.
func TestACL_LegacyIndexIsAdminPrivate(t *testing.T) {
	reg := aclRegistry(t)
	if _, err := reg.Get("old"); err != nil { // no acl.json written
		t.Fatal(err)
	}
	if _, err := reg.Read(as("bob"), "old"); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("bob reading a legacy index: err=%v", err)
	}
	if _, err := reg.Read(as("admin"), "old"); err != nil {
		t.Fatalf("admin reading a legacy index: %v", err)
	}
	// No principal at all is nobody.
	if reg.CanRead(context.Background(), "old") {
		t.Fatal("a context with no principal could read")
	}
}

// Read must never create: a probe for a name must not leave an index behind.
func TestACL_ReadDoesNotCreate(t *testing.T) {
	reg := aclRegistry(t)
	if _, err := reg.Read(as("bob"), "ghost"); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err=%v", err)
	}
	if reg.exists("ghost") {
		t.Fatal("Read created the index")
	}
	if err := reg.DeleteAs(as("bob"), "ghost", false); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("delete of a missing index: err=%v", err)
	}
}

func TestACL_DeleteIsOwnerOnly(t *testing.T) {
	reg := aclRegistry(t)
	if _, err := reg.Write(as("alice"), "a"); err != nil {
		t.Fatal(err)
	}
	must(t, reg.SetVisibility(as("alice"), "a", Public))
	if err := reg.DeleteAs(as("bob"), "a", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob deleting alice's index: err=%v", err)
	}
	if err := reg.DeleteAs(as("alice"), "a", false); err != nil {
		t.Fatal(err)
	}
}

func TestACL_TransferOwnership(t *testing.T) {
	reg := aclRegistry(t)
	if _, err := reg.Write(as("alice"), "base"); err != nil {
		t.Fatal(err)
	}
	must(t, reg.SetVisibility(as("alice"), "base", Public))
	if err := reg.TransferOwnership(as("bob"), "base", "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob taking alice's index: err=%v", err)
	}
	must(t, reg.TransferOwnership(as("alice"), "base", "maintainer"))
	if a := reg.ACLOf("base"); a.Owner != "maintainer" || a.Visibility != Public {
		t.Fatalf("after transfer: %+v", a)
	}
	if _, err := reg.Write(as("alice"), "base"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("former owner writing: err=%v", err)
	}
	// The admin may move any index — a private one too — e.g. to recover one
	// from a departed owner.
	must(t, reg.SetVisibility(as("maintainer"), "base", Private))
	must(t, reg.TransferOwnership(as("admin"), "base", "alice"))
	if _, err := reg.Write(as("alice"), "base"); err != nil {
		t.Fatal(err)
	}
}
