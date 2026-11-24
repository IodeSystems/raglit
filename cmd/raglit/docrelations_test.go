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

// The Relations pane needs relations and slices for a document, named by index
// and display path — the browser cannot name the PROJECT DIRECTORY that
// /api/relations and /api/slices take, because that is a host path. So the
// daemon resolves it, the way /api/doc-detail already does. This test is the
// record of that: a request carrying no host path gets the recorded rulings
// and slices back.
func TestDocRelations_ResolvesServerSide(t *testing.T) {
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })

	corpus := filepath.Join(root, "corpus")
	must(t, os.MkdirAll(corpus, 0o755))
	a := filepath.Join(corpus, "a.md")
	b := filepath.Join(corpus, "b.md")
	for _, p := range []string{a, b} {
		must(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	ctx := context.Background()
	st, _ := reg.Get("case")
	must(t, st.Ingest(ctx, raglit.Document{Path: a, Title: "a.md", Fragments: []raglit.Fragment{{Text: "alpha"}}}))
	must(t, st.Ingest(ctx, raglit.Document{Path: b, Title: "b.md", Fragments: []raglit.Fragment{{Text: "bravo"}}}))

	// Record a ruling and a slice under the corpus root, the way the CLI does.
	js, err := raglit.OpenJudgements(raglit.AuditPath(corpus))
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	must(t, js.PutRelation(raglit.Mark{A: a, B: b, Kind: raglit.MarkVersion, Supersedes: b, Coverage: 0.9}))
	must(t, js.PutSlice(raglit.Slice{ID: "s1", Parent: a, From: 3, To: 6, Title: "Exhibit A"}))

	mw, tokenPath, err := daemonAuth(ctx, reg, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	adminTokenPath = tokenPath
	t.Cleanup(func() { adminTokenPath = "" })
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(raglit.Home(root))
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, nil, raglit.GCPolicy{}, nil, ingestFragParams{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mw(h))
	t.Cleanup(srv.Close)
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(string(raw))

	// The request names the INDEX and the DISPLAY path — no host path.
	u := srv.URL + "/api/doc-relations?" + url.Values{"index": {"case"}, "path": {"a.md"}}.Encode()
	code, body := doAs(t, "GET", u, tok, "")
	if code != http.StatusOK {
		t.Fatalf("doc-relations: %d %s", code, clip(body, 300))
	}
	var got struct {
		Relations []struct {
			A          string  `json:"a"`
			B          string  `json:"b"`
			Kind       string  `json:"kind"`
			Supersedes string  `json:"supersedes"`
			Coverage   float64 `json:"coverage"`
		} `json:"relations"`
		Slices []struct {
			ID     string `json:"id"`
			Parent string `json:"parent"`
			From   int    `json:"from"`
			To     int    `json:"to"`
			Title  string `json:"title"`
		} `json:"slices"`
	}
	if json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("unmarshal: %s", clip(body, 300))
	}
	if len(got.Relations) != 1 {
		t.Fatalf("relations: %d, want 1", len(got.Relations))
	}
	r := got.Relations[0]
	if r.Kind != "version" || r.Supersedes != b {
		t.Errorf("relation: %+v, want version supersedes b.md", r)
	}
	if r.Coverage != 0.9 {
		t.Errorf("coverage: %v, want 0.9", r.Coverage)
	}
	if len(got.Slices) != 1 {
		t.Fatalf("slices: %d, want 1", len(got.Slices))
	}
	s := got.Slices[0]
	if s.ID != "s1" || s.From != 3 || s.To != 6 || s.Title != "Exhibit A" {
		t.Errorf("slice: %+v", s)
	}
	if s.Parent != a {
		t.Errorf("slice parent: %q, want %q", s.Parent, a)
	}

	// A display path the index does not hold is refused, not resolved.
	u2 := srv.URL + "/api/doc-relations?" + url.Values{"index": {"case"}, "path": {"nope.md"}}.Encode()
	if code, _ := doAs(t, "GET", u2, tok, ""); code != http.StatusBadRequest {
		t.Fatalf("unknown document: %d, want 400", code)
	}
}
