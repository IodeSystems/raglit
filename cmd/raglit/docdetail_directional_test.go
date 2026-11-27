package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// sp: /api/doc-detail returns directional relations with direction and locator,
// and the UI renders them at top of the document.
//
// The store layer (DirectionalRelationsFor) is tested in relations_test.go.
// This is the composition test: it records a corrected-by and an
// invalidated-by ruling, then asks the daemon for document detail and
// verifies the relations appear in the payload with correct direction
// (doc_is_a) and locator, and that a document with no directional rulings
// has an empty/absent field.
func TestDocDetailReturnsDirectionalRelations(t *testing.T) {
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })

	corpus := filepath.Join(root, "corpus")
	must(t, os.MkdirAll(corpus, 0o755))
	// Three documents: a.md (the corrected one), b.md (the corrector), c.md (the invalidator).
	a := filepath.Join(corpus, "a.md")
	b := filepath.Join(corpus, "b.md")
	c := filepath.Join(corpus, "c.md")
	for _, p := range []string{a, b, c} {
		must(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	ctx := context.Background()
	st, _ := reg.Get("case")
	must(t, st.Ingest(ctx, raglit.Document{Path: a, Title: "a.md", Fragments: []raglit.Fragment{{Text: "alpha"}}}))
	must(t, st.Ingest(ctx, raglit.Document{Path: b, Title: "b.md", Fragments: []raglit.Fragment{{Text: "bravo"}}}))
	must(t, st.Ingest(ctx, raglit.Document{Path: c, Title: "c.md", Fragments: []raglit.Fragment{{Text: "charlie"}}}))

	// Record directional rulings:
	//   a.md is corrected-by b.md (a is A, b is B; part grain: page 2)
	//   a.md is invalidated-by c.md (a is A, c is B; whole document)
	js, err := raglit.OpenJudgements(raglit.AuditPath(corpus))
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	must(t, js.PutRelation(raglit.Mark{A: a, B: b, Kind: raglit.MarkCorrectedBy, Locator: raglit.Locator{Page: 2}, Note: "shed location"}))
	must(t, js.PutRelation(raglit.Mark{A: a, B: c, Kind: raglit.MarkInvalidatedBy}))

	mw, tokenPath, err := daemonAuth(ctx, reg, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(raglit.Home(root))
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, nil, raglit.GCPolicy{}, nil, ingestFragParams{})
	if err != nil {
		t.Fatalf("buildGatHandler: %v", err)
	}
	srv := httptest.NewServer(mw(h))
	t.Cleanup(srv.Close)
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(string(raw))

	// Helper to fetch doc-detail and unmarshal the directional_relations field.
	type drOut struct {
		A        string `json:"a"`
		B        string `json:"b"`
		Kind     string `json:"kind"`
		DocIsA   bool   `json:"doc_is_a"`
		Locator  struct {
			Region string `json:"region"`
			Page   int    `json:"page"`
			Unit   string `json:"unit"`
		} `json:"locator"`
		Note string `json:"note"`
		By   string `json:"by"`
		At   string `json:"at"`
	}
	type detailOut struct {
		Path                 string   `json:"path"`
		DirectionalRelations []drOut  `json:"directional_relations"`
	}
	fetch := func(docPath string) detailOut {
		t.Helper()
		u := srv.URL + "/api/doc-detail?" + url.Values{"index": {"case"}, "path": {docPath}}.Encode()
		code, body := doAs(t, "GET", u, tok, "")
		if code != http.StatusOK {
			t.Fatalf("doc-detail %q: %d %s", docPath, code, clip(body, 300))
		}
		var d detailOut
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatalf("unmarshal: %v: %s", err, clip(body, 300))
		}
		return d
	}

	// a.md is the corrected AND invalidated side (A in both rulings).
	da := fetch("a.md")
	if len(da.DirectionalRelations) != 2 {
		t.Fatalf("a.md: %d directional relations, want 2", len(da.DirectionalRelations))
	}
	// Find the corrected-by relation.
	var cb, ib *drOut
	for i := range da.DirectionalRelations {
		r := &da.DirectionalRelations[i]
		switch r.Kind {
		case "corrected-by":
			cb = r
		case "invalidated-by":
			ib = r
		}
	}
	if cb == nil {
		t.Fatal("a.md: no corrected-by relation found")
	}
	if ib == nil {
		t.Fatal("a.md: no invalidated-by relation found")
	}
	// corrected-by: a.md is A (the corrected side), b.md is B.
	if !cb.DocIsA {
		t.Errorf("corrected-by: doc_is_a=false for a.md, want true (a.md is the corrected side)")
	}
	// The other side should be b.md's display path.
	wantB := "b.md"
	if cb.B != wantB {
		t.Errorf("corrected-by: b=%q, want %q", cb.B, wantB)
	}
	// Locator should carry page 2.
	if cb.Locator.Page != 2 {
		t.Errorf("corrected-by: locator.page=%d, want 2", cb.Locator.Page)
	}
	if cb.Note != "shed location" {
		t.Errorf("corrected-by: note=%q, want %q", cb.Note, "shed location")
	}
	// invalidated-by: a.md is A, c.md is B, no locator.
	if !ib.DocIsA {
		t.Errorf("invalidated-by: doc_is_a=false for a.md, want true")
	}
	if ib.B != "c.md" {
		t.Errorf("invalidated-by: b=%q, want c.md", ib.B)
	}
	if ib.Locator.Page != 0 && ib.Locator.Region != "" && ib.Locator.Unit != "" {
		t.Errorf("invalidated-by: locator set, want empty: %+v", ib.Locator)
	}

	// b.md is the correcting side (B) in the corrected-by ruling: doc_is_a=false.
	db := fetch("b.md")
	if len(db.DirectionalRelations) != 1 {
		t.Fatalf("b.md: %d directional relations, want 1", len(db.DirectionalRelations))
	}
	rb := &db.DirectionalRelations[0]
	if rb.Kind != "corrected-by" {
		t.Fatalf("b.md: kind=%q, want corrected-by", rb.Kind)
	}
	if rb.DocIsA {
		t.Errorf("b.md: doc_is_a=true, want false (b.md is the corrector, not the corrected side)")
	}
	if rb.A != "a.md" {
		t.Errorf("b.md: a=%q, want a.md", rb.A)
	}

	// c.md is the invalidating side (B): doc_is_a=false.
	dc := fetch("c.md")
	if len(dc.DirectionalRelations) != 1 {
		t.Fatalf("c.md: %d directional relations, want 1", len(dc.DirectionalRelations))
	}
	rc := &dc.DirectionalRelations[0]
	if rc.Kind != "invalidated-by" {
		t.Fatalf("c.md: kind=%q, want invalidated-by", rc.Kind)
	}
	if rc.DocIsA {
		t.Errorf("c.md: doc_is_a=true, want false (c.md is the invalidator)")
	}

	// A document with no directional rulings has no directional_relations field.
	// Use the fixture's built-in /notes.md document (ingested by gatTestServer is
	// not available here; instead, ingest a fourth document with no rulings).
	d4 := filepath.Join(corpus, "d.md")
	must(t, os.WriteFile(d4, []byte("y"), 0o644))
	must(t, st.Ingest(ctx, raglit.Document{Path: d4, Title: "d.md", Fragments: []raglit.Fragment{{Text: "delta"}}}))
	dd := fetch("d.md")
	if len(dd.DirectionalRelations) != 0 {
		t.Errorf("d.md: %d directional relations, want 0 (no rulings recorded)", len(dd.DirectionalRelations))
	}
}
