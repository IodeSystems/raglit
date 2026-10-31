package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// adminOnlyTestServer is a daemon with no OIDC issuer: admin-token mode. It
// returns the server, its registry and the admin token.
func adminOnlyTestServer(t *testing.T) (*httptest.Server, *raglit.Registry, string) {
	t.Helper()
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, _ := reg.Get("notes")
	must(t, st.Ingest(ctx, raglit.Document{Path: "file:///n.md", Title: "N",
		Fragments: []raglit.Fragment{{Text: "walrus notes"}}}))
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
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, nil, raglit.GCPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mw(h))
	t.Cleanup(func() { srv.Close(); reg.Close() })
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	return srv, reg, strings.TrimSpace(string(raw))
}

// USER 2026-10-30: "Raglit should be admin-token or oidc, never open." With
// no issuer configured, every surface but health still needs the admin token.
func TestNeverOpen_NoIssuerMeansAdminToken(t *testing.T) {
	srv, _, tok := adminOnlyTestServer(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/indexes", ""},
		{"GET", "/search?q=walrus", ""},
		{"POST", "/graphql", `{"query":"{__typename}"}`},
		{"GET", "/api/branches", ""},
		{"GET", "/", ""},
		{"GET", "/openapi.json", ""},
	} {
		if code, _ := doAs(t, c.method, srv.URL+c.path, "", c.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", c.method, c.path, code)
		}
		if code, _ := doAs(t, c.method, srv.URL+c.path, "wrong", c.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a wrong token: %d, want 401", c.method, c.path, code)
		}
	}
	var health struct {
		Auth       bool   `json:"auth"`
		AdminToken string `json:"admin_token"`
	}
	code, hb := doAs(t, "GET", srv.URL+"/api/health", "", "")
	if code != http.StatusOK || json.Unmarshal([]byte(hb), &health) != nil || !health.Auth || health.AdminToken == "" {
		t.Fatalf("health without a token: %d %s, want 200 naming the token file", code, hb)
	}
	if code, body := doAs(t, "GET", srv.URL+"/search?q=walrus&index=notes", tok, ""); code != http.StatusOK || !strings.Contains(body, "walrus") {
		t.Fatalf("admin token search: %d %s", code, clip(body, 200))
	}
}

// With the ACL on, a read of a name that is not an index answers 404 and
// creates nothing: the empty shim indexes a wrong selector used to leave.
func TestNeverOpen_ReadingAMissingIndexCreatesNothing(t *testing.T) {
	srv, reg, tok := adminOnlyTestServer(t)
	before := strings.Join(reg.Names(), ",")
	if code, body := doAs(t, "GET", srv.URL+"/search?q=walrus&index=no-such", tok, ""); code != http.StatusNotFound || !strings.Contains(body, `no index \"no-such\"`) {
		t.Fatalf("search of a missing index: %d %s, want 404 naming it", code, clip(body, 300))
	}
	// A near name is offered in selector form.
	if code, body := doAs(t, "GET", srv.URL+"/search?q=walrus&index=note", tok, ""); code != http.StatusNotFound || !strings.Contains(body, ":notes") {
		t.Fatalf("search of a near name: %d %s, want 404 suggesting :notes", code, clip(body, 300))
	}
	if after := strings.Join(reg.Names(), ","); after != before {
		t.Fatalf("indexes %q became %q after reading a missing one", before, after)
	}
}
