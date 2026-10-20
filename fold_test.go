package raglit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFoldProject_PublicOnlyDedupedWithProvenance(t *testing.T) {
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
	must(t, reg.TransferOwnership(as("alice"), a.Base, "maint"))

	drain := func(index string) {
		t.Helper()
		st, err := reg.Existing(index)
		if err != nil {
			t.Fatal(err)
		}
		w := &Worker{Store: st}
		for range 20 {
			did, err := w.ProcessOne(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !did {
				return
			}
		}
	}
	upload := func(who, index, name, text string) string {
		t.Helper()
		st, err := reg.Write(as(who), index)
		if err != nil {
			t.Fatal(err)
		}
		p, err := st.SaveUpload(name, []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnqueueFresh(UploadURL(p), "", false); err != nil {
			t.Fatal(err)
		}
		drain(index)
		return UploadURL(p)
	}
	shared := upload("alice", a.Public, "spec.md", "the shared deployment spec, version one")
	upload("bob", b.Public, "spec-copy.md", "the shared deployment spec, version one")
	upload("alice", a.Private, "secret.md", "alice private plan nobody else may see")
	pub, _ := reg.Write(as("alice"), a.Public)
	if _, err := pub.AddNote(shared, Note{Author: "alice", Body: "checked against the runbook"}); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.FoldProject(as("bob"), "proj", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob folding a base he does not own: err=%v", err)
	}
	dry, err := reg.FoldProject(as("maint"), "proj", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Folded) != 1 || dry.AlreadyInBase != 1 {
		t.Fatalf("dry run: %+v", dry)
	}
	for _, l := range dry.LayersScanned {
		if l == a.Private || l == b.Private {
			t.Fatalf("a private layer was read: %v", dry.LayersScanned)
		}
	}
	base, _ := reg.Existing(a.Base)
	if costs, _ := base.DocCosts(); len(costs) != 0 {
		t.Fatal("a dry run wrote to the base")
	}

	rep, err := reg.FoldProject(as("maint"), "proj", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Folded) != 1 || rep.Pending != 0 {
		t.Fatalf("fold: %+v", rep)
	}
	drain(a.Base)
	again, err := reg.FoldProject(as("maint"), "proj", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Folded) != 0 || again.Settled != 1 {
		t.Fatalf("second run should fold nothing and settle one: %+v", again)
	}
	notes, err := base.Notes(rep.Folded[0].BasePath)
	if err != nil {
		t.Fatal(err)
	}
	var prov, copied bool
	for _, n := range notes {
		prov = prov || strings.Contains(n.Body, "Folded from layer "+a.Public)
		copied = copied || (n.Author == "alice" && n.Body == "checked against the runbook")
	}
	if !prov || !copied {
		t.Fatalf("base notes %+v; want provenance and alice's copied note", notes)
	}
	if h, _ := base.Search("private plan", 5); len(h) != 0 {
		t.Fatal("private content reached the base")
	}
}
