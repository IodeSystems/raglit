package raglit

import (
	"context"
	"testing"
)

// Two people import the same document into their own layers over an empty base:
// one copy is the cost a maintained base would have spared.
func TestProjectSavings_CountsCopiesBeyondTheFirst(t *testing.T) {
	reg := aclRegistry(t)
	ctx := context.Background()
	a, err := reg.RegisterProject(as("alice"), "proj", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := reg.RegisterProject(as("bob"), "proj", "")
	if err != nil {
		t.Fatal(err)
	}
	put := func(who, index, path, hash, text string) {
		t.Helper()
		st, err := reg.Write(as(who), index)
		if err != nil {
			t.Fatal(err)
		}
		must(t, st.Ingest(ctx, Document{Path: path, Title: path, Fragments: []Fragment{{Text: text}, {Text: text + " again"}}}))
		must(t, st.SetDocumentHash(path, hash))
	}
	put("alice", a.Public, "file:///a/spec.md", "h-spec", "the shared spec")
	put("bob", b.Public, "file:///b/spec.md", "h-spec", "the shared spec")
	put("bob", b.Public, "file:///b/own.md", "h-own", "only bob has this")

	rep, err := reg.ProjectSavings(as("carol"), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if rep.DuplicateDocuments != 1 || rep.DuplicatePassages != 2 {
		t.Fatalf("copies: %+v", rep)
	}
	if !rep.Recommend || rep.FanoutWithBase != 2 || rep.FanoutNow < 3 {
		t.Fatalf("recommendation: %+v", rep)
	}
	// Carol reads only the public layers; the private ones do not count.
	for _, l := range rep.Layers {
		if l.Name == a.Private || l.Name == b.Private {
			t.Fatalf("a private layer was counted for carol: %+v", rep.Layers)
		}
	}

	// A base that holds documents of its own is already maintained.
	put("alice", a.Base, "file:///base/spec.md", "h-spec", "the shared spec")
	if rep, _ = reg.ProjectSavings(as("carol"), "proj"); rep.Recommend {
		t.Fatalf("a base with documents should not be recommended again: %+v", rep)
	}
}
