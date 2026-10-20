package raglit

import (
	"context"
	"sync"
	"testing"
)

func TestRegisterProject_BaseThenLayers(t *testing.T) {
	reg := aclRegistry(t)
	ctx := context.Background()
	const id = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

	a, err := reg.RegisterProject(as("alice"), id, "github.com/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if !a.NewBase || !a.NewLayer || a.Layer == a.Base {
		t.Fatalf("first registration = %+v; want a new base AND a layer", a)
	}
	if acl := reg.ACLOf(a.Base); acl.Visibility != Public {
		t.Fatalf("base ACL %+v; a private base makes every other layer unreadable", acl)
	}
	if acl := reg.ACLOf(a.Layer); acl.Owner != "alice" || acl.Visibility != Private {
		t.Fatalf("alice's layer ACL %+v", acl)
	}
	again, err := reg.RegisterProject(as("alice"), id, "")
	if err != nil || again.Base != a.Base || again.Layer != a.Layer || again.NewBase || again.NewLayer {
		t.Fatalf("repeat registration = %+v, %v; want the same pair", again, err)
	}
	b, err := reg.RegisterProject(as("bob"), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Base != a.Base || b.NewBase || b.Layer == a.Layer {
		t.Fatalf("bob = %+v; want alice's base and his own layer", b)
	}
	if reg.ProjectBase(id) != a.Base {
		t.Fatal("lookup by project id failed")
	}

	// Private by default: bob does not see alice's layer. Public: he does, as a
	// sibling of his own.
	al, _ := reg.Write(as("alice"), a.Layer)
	must(t, al.Ingest(ctx, Document{Path: "file:///n.md", Title: "N", Fragments: []Fragment{{Text: "alice narwhal"}}}))
	if s := reg.PublicSiblings(as("bob"), b.Layer); len(s) != 0 {
		t.Fatalf("bob sees private siblings: %v", s)
	}
	must(t, reg.SetVisibility(as("alice"), a.Layer, Public))
	s := reg.PublicSiblings(as("bob"), b.Layer)
	if len(s) != 1 || s[0] != a.Layer {
		t.Fatalf("bob's public siblings = %v, want [%s]", s, a.Layer)
	}
	st, _ := reg.Read(as("bob"), s[0])
	if h, _ := st.SearchOwnLayer(ctx, "narwhal", "", "", 5); len(h) != 1 {
		t.Fatalf("sibling layer search: %d hits", len(h))
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
