package main

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// Scopes round-trip over HTTP and show in the index list; a claim sets them
// only when they were never set.
func TestScopeIndexOp(t *testing.T) {
	srv, _ := settingsServer(t, `{}`)
	post := func(body string) string {
		code, out := doAs(t, "POST", srv.URL+"/api/index/scope?index=notes", "", body)
		if code != http.StatusOK {
			t.Fatalf("scope %s: %d %s", body, code, out)
		}
		return out
	}
	if out := post(`{"claim":"/src/app"}`); !strings.Contains(out, `"scopes":["/src/app"]`) {
		t.Fatalf("a claim on an unscoped index must set it: %s", out)
	}
	if out := post(`{"claim":"/src/other"}`); !strings.Contains(out, `"scopes":["/src/app"]`) {
		t.Fatalf("a claim must not move a set scope: %s", out)
	}
	if out := post(`{"add":["/src/lib/"],"remove":["/src/app"]}`); !strings.Contains(out, `"scopes":["/src/lib"]`) {
		t.Fatalf("add/remove: %s", out)
	}
	if out := post(`{"scopes":[],"ambient":true}`); !strings.Contains(out, `"scopes":[]`) || !strings.Contains(out, `"ambient":true`) {
		t.Fatalf("global ambient: %s", out)
	}
	if _, body := doAs(t, "GET", srv.URL+"/indexes", "", ""); !strings.Contains(body, `"ambient":true`) {
		t.Fatalf("index list does not show ambient: %s", body)
	}
}

// "@cwd:DIR" selects by scope; a bare name finds its one namespaced index.
func TestSelectIndexesByScopeAndLocalName(t *testing.T) {
	reg, err := raglit.OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	for name, scope := range map[string]string{"app__code": "/src/app", "other__code": "/src/other", "app__notes": "/src/app"} {
		st, _ := reg.Get(name)
		st.SetScopes([]string{scope})
	}
	ctx := context.Background()
	if got := selectIndexes(ctx, reg, "@cwd:/src/app/web"); !reflect.DeepEqual(got, []string{"app__code", "app__notes"}) {
		t.Errorf("@cwd = %q", got)
	}
	if got := selectIndexes(ctx, reg, "notes"); !reflect.DeepEqual(got, []string{"app__notes"}) {
		t.Errorf("unique local name = %q, want app__notes", got)
	}
	if got := selectIndexes(ctx, reg, "code"); !reflect.DeepEqual(got, []string{"code"}) {
		t.Errorf("ambiguous local name = %q, want it left as given", got)
	}
}

// The migration: a watched project's directory, else a project-like document
// root, else an opt-in global; and it runs once.
func TestMigrateScopes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RAGLIT_ROOT", root)
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	proj := t.TempDir()
	home := raglit.Home(filepath.Join(proj, ".raglit"))
	must(t, home.Ensure())
	must(t, raglit.SaveConfig(home, raglit.Config{Project: "app"}))
	for _, n := range []string{"app__code", "loose__x", "life"} {
		if _, err := reg.Get(n); err != nil {
			t.Fatal(err)
		}
	}
	migrateScopes(reg, []string{string(home)})
	want := map[string][]string{"app__code": {proj}, "loose__x": {}, "life": {}}
	for n, w := range want {
		st, _ := reg.Get(n)
		got, set := st.Scopes()
		if !set || !reflect.DeepEqual(got, w) {
			t.Errorf("%s scopes = %q (set %v), want %q", n, got, set, w)
		}
	}
	st, _ := reg.Get("app__code")
	st.SetScopes([]string{"/moved"})
	migrateScopes(reg, []string{string(home)})
	if got, _ := st.Scopes(); !reflect.DeepEqual(got, []string{"/moved"}) {
		t.Errorf("a second migration changed set scopes: %q", got)
	}
}

func TestProjectLike(t *testing.T) {
	for root, want := range map[string]bool{"": false, "/": false, "/home/u": false, "/home": false, "/home/u/src/app": true, "rel": false} {
		if got := projectLike(root, "/home/u"); got != want {
			t.Errorf("projectLike(%q) = %v, want %v", root, got, want)
		}
	}
}
