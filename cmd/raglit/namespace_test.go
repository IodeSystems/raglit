package main

import (
	"context"
	"flag"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

func TestNSIndex(t *testing.T) {
	cases := []struct{ ns, local, want string }{
		{"acme", "code", "acme__code"},
		{"acme", "", "acme__default"},
		{"", "code", "code"},
		{"", "", "default"},
	}
	for _, c := range cases {
		if got := nsIndex(c.ns, c.local); got != c.want {
			t.Errorf("nsIndex(%q,%q) = %q, want %q", c.ns, c.local, got, c.want)
		}
	}
}

func TestNSSelector(t *testing.T) {
	cases := []struct{ ns, sel, want string }{
		{"acme", "", "acme__*"},
		{"acme", "all", "acme__*"},
		{"acme", "code", "acme__code"},
		{"acme", "code,docs", "acme__code,acme__docs"},
		{"", "code", "code"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := nsSelector(c.ns, c.sel); got != c.want {
			t.Errorf("nsSelector(%q,%q) = %q, want %q", c.ns, c.sel, got, c.want)
		}
	}
}

func TestNSReadSelector(t *testing.T) {
	cases := []struct {
		ns     string
		shared []string
		sel    string
		want   string
	}{
		{"acme", nil, "", "acme__*"},
		{"acme", []string{"shared"}, "", "acme__*,shared__*"},
		{"acme", []string{"shared", "acme"}, "all", "acme__*,shared__*"}, // own filtered from shared
		{"acme", []string{"shared"}, "code", "acme__code"},               // explicit → private only
		{"", []string{"shared"}, "", ""},
	}
	for _, c := range cases {
		if got := nsReadSelector(c.ns, c.shared, c.sel); got != c.want {
			t.Errorf("nsReadSelector(%q,%v,%q) = %q, want %q", c.ns, c.shared, c.sel, got, c.want)
		}
	}
}

func TestNSStripAndJSON(t *testing.T) {
	if got := nsStrip("acme", "acme__code"); got != "code" {
		t.Errorf("nsStrip = %q", got)
	}
	if got := nsStrip("acme", "other__code"); got != "other__code" {
		t.Errorf("nsStrip should not touch a foreign prefix: %q", got)
	}
	in := `{"hits":[{"index":"acme__code","doc_id":"a"},{"index":"acme__docs","doc_id":"b"}]}`
	got := string(stripNSJSON([]byte(in), "acme"))
	if strings.Contains(got, "acme__") {
		t.Errorf("stripNSJSON left a prefix: %s", got)
	}
	if !strings.Contains(got, `"index":"code"`) || !strings.Contains(got, `"index":"docs"`) {
		t.Errorf("stripNSJSON wrong result: %s", got)
	}
}

func TestFilterIndexList(t *testing.T) {
	in := `{"indexes":[{"name":"acme__code","documents":2},{"name":"other__docs","documents":9},{"name":"acme__docs","documents":1}]}`
	got := string(filterIndexList([]byte(in), "acme", nil))
	if strings.Contains(got, "other") {
		t.Errorf("filterIndexList leaked a foreign index: %s", got)
	}
	if !strings.Contains(got, `"name":"code"`) || !strings.Contains(got, `"name":"docs"`) {
		t.Errorf("filterIndexList wrong result: %s", got)
	}
}

// TestSelectIndexesWildcard: a "<prefix>*" arg expands to only the matching
// indexes on the daemon.
func TestSelectIndexesWildcard(t *testing.T) {
	reg, err := raglit.OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	for _, name := range []string{"acme__code", "acme__docs", "other__code"} {
		if _, err := reg.Get(name); err != nil {
			t.Fatal(err)
		}
	}
	got := selectIndexes(reg, "acme__*")
	if len(got) != 2 {
		t.Fatalf("acme__* = %v, want 2 acme indexes", got)
	}
	for _, n := range got {
		if !strings.HasPrefix(n, "acme__") {
			t.Errorf("wildcard leaked %q", n)
		}
	}
}

// TestServeClient_NamespaceIsolation: two projects' indexes live on one daemon;
// a client scoped to "acme" sees only acme's docs and index list, never other's.
func TestServeClient_NamespaceIsolation(t *testing.T) {
	home := raglit.Home(t.TempDir())
	reg, err := raglit.OpenScopedRegistry(string(home))
	if err != nil {
		t.Fatal(err)
	}
	seed := func(index, path, text string) {
		st, err := reg.Get(index)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Ingest(context.Background(), raglit.Document{
			Path: path, Fragments: []raglit.Fragment{{Text: text}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("acme__default", "file:///acme-secret.md", "acme quarterly revenue is confidential")
	seed("other__default", "file:///other-secret.md", "other quarterly revenue is confidential")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(home)
	h, err := buildGatHandler(reg, lf, home, 8, nil, raglit.GCPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); reg.Close() })

	acme := daemonToolHandlers(srv.URL, 8, "acme", nil)

	// search-all (empty index → acme__*) finds acme's doc, not other's.
	out := callTool(t, acme.search, map[string]any{"query": "quarterly revenue"})
	if !strings.Contains(out, "acme-secret.md") {
		t.Fatalf("acme search should find its own doc: %s", out)
	}
	if strings.Contains(out, "other-secret.md") {
		t.Fatalf("LEAK: acme search returned other project's doc: %s", out)
	}
	// index tag is un-namespaced for display.
	if strings.Contains(out, "acme__") {
		t.Fatalf("index tag not stripped: %s", out)
	}
	// list_indexes shows only acme's index (as "default"), not other's.
	list := callTool(t, acme.listIndexes, nil)
	if !strings.Contains(list, `"name":"default"`) || strings.Contains(list, "other") || strings.Contains(list, "acme__") {
		t.Fatalf("list_indexes not scoped/stripped: %s", list)
	}
}

// TestServeClient_SharedNamespace: a project reading a shared namespace sees its
// own docs AND the shared ones, but still not an unrelated project's.
func TestServeClient_SharedNamespace(t *testing.T) {
	home := raglit.Home(t.TempDir())
	reg, err := raglit.OpenScopedRegistry(string(home))
	if err != nil {
		t.Fatal(err)
	}
	seed := func(index, path, text string) {
		st, err := reg.Get(index)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Ingest(context.Background(), raglit.Document{
			Path: path, Fragments: []raglit.Fragment{{Text: text}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("acme__default", "file:///acme.md", "confidential acme note")
	seed("shared__default", "file:///handbook.md", "confidential shared handbook")
	seed("other__default", "file:///other.md", "confidential other note")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(home)
	h, err := buildGatHandler(reg, lf, home, 8, nil, raglit.GCPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); reg.Close() })

	acme := daemonToolHandlers(srv.URL, 8, "acme", []string{"shared"})
	out := callTool(t, acme.search, map[string]any{"query": "confidential"})
	if !strings.Contains(out, "acme.md") {
		t.Fatalf("should see own doc: %s", out)
	}
	if !strings.Contains(out, "handbook.md") {
		t.Fatalf("should see shared doc: %s", out)
	}
	if strings.Contains(out, "other.md") {
		t.Fatalf("LEAK: saw an unrelated project's doc: %s", out)
	}
	// list_indexes: own shown as "default", shared kept as "shared__default".
	list := callTool(t, acme.listIndexes, nil)
	if !strings.Contains(list, `"name":"default"`) || !strings.Contains(list, `"name":"shared__default"`) || strings.Contains(list, "other") {
		t.Fatalf("list_indexes shared scoping wrong: %s", list)
	}
}
