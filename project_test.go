package raglit

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestRegisterProject_BaseThenTwoLayersEach(t *testing.T) {
	reg := aclRegistry(t)
	ctx := context.Background()
	const id = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

	a, err := reg.RegisterProject(as("alice"), id, "github.com/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if !a.NewBase || !a.NewPrivate || !a.NewPublic {
		t.Fatalf("first registration = %+v; want a new base and both layers", a)
	}
	for _, n := range []string{a.Base, a.Private, a.Public} {
		if !strings.HasPrefix(n, a.Namespace+"__") || strings.Contains(a.Namespace, "_") {
			t.Fatalf("%q is not in namespace %q", n, a.Namespace)
		}
	}
	if acl := reg.ACLOf(a.Base); acl.Visibility != Public {
		t.Fatalf("base ACL %+v; a private base makes every other layer unreadable", acl)
	}
	if acl := reg.ACLOf(a.Private); acl.Owner != "alice" || acl.Visibility != Private {
		t.Fatalf("alice's private layer ACL %+v", acl)
	}
	if acl := reg.ACLOf(a.Public); acl.Owner != "alice" || acl.Visibility != Public {
		t.Fatalf("alice's public layer ACL %+v", acl)
	}
	again, err := reg.RegisterProject(as("alice"), id, "")
	if err != nil || again.Base != a.Base || again.Private != a.Private || again.Public != a.Public ||
		again.NewBase || again.NewPrivate || again.NewPublic {
		t.Fatalf("repeat registration = %+v, %v; want the same names, nothing new", again, err)
	}
	b, err := reg.RegisterProject(as("bob"), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Base != a.Base || b.NewBase || b.Private == a.Private || b.Public == a.Public {
		t.Fatalf("bob = %+v; want alice's base and his own layers", b)
	}
	if reg.ProjectBase(id) != a.Base {
		t.Fatal("lookup by project id failed")
	}

	// One public note does not expose the private ones.
	priv, _ := reg.Write(as("alice"), a.Private)
	must(t, priv.Ingest(ctx, Document{Path: "file:///p.md", Title: "P", Fragments: []Fragment{{Text: "alice secret narwhal"}}}))
	pub, _ := reg.Write(as("alice"), a.Public)
	must(t, pub.Ingest(ctx, Document{Path: "file:///s.md", Title: "S", Fragments: []Fragment{{Text: "alice shared narwhal"}}}))

	sib := map[string]bool{}
	for _, s := range reg.Siblings(as("bob"), b.Private) {
		sib[s] = true
	}
	if !sib[a.Public] || sib[a.Private] || !sib[b.Public] {
		t.Fatalf("bob's siblings = %v; want alice's public + his own public, not alice's private", sib)
	}
	st, _ := reg.Read(as("bob"), a.Public)
	if h, _ := st.SearchOwnLayer(ctx, "narwhal", "", "", 5); len(h) != 1 || h[0].Path != "file:///s.md" {
		t.Fatalf("alice's public layer search: %+v", h)
	}
}

// With access control off, a branch's siblings are every other branch on its
// parent.
func TestSiblings_ACLOff(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if _, err := reg.Get("main"); err != nil {
		t.Fatal(err)
	}
	must(t, reg.ForkBranch("x", "main"))
	must(t, reg.ForkBranch("y", "main"))
	if s := reg.Siblings(context.Background(), "x"); len(s) != 1 || s[0] != "y" {
		t.Fatalf("siblings of x = %v", s)
	}
	if s := reg.Siblings(context.Background(), "main"); len(s) != 0 {
		t.Fatalf("a root index has no siblings: %v", s)
	}
}

// Two first-time registrants racing end on one base.
func TestRegisterProject_RaceMakesOneBase(t *testing.T) {
	reg := aclRegistry(t)
	var wg sync.WaitGroup
	bases := make([]string, 8)
	for i := range bases {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := reg.RegisterProject(as(string(rune('a'+i))), "same-id", "")
			if err != nil {
				t.Error(err)
			}
			bases[i] = r.Base
		}(i)
	}
	wg.Wait()
	for _, b := range bases {
		if b != bases[0] {
			t.Fatalf("bases differ: %v", bases)
		}
	}
}
