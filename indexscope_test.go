package raglit

import (
	"context"
	"reflect"
	"testing"
)

func TestScopeMatches(t *testing.T) {
	cases := []struct {
		scopes []string
		dir    string
		want   bool
	}{
		{[]string{"/src/app"}, "/src/app", true},
		{[]string{"/src/app"}, "/src/app/web", true},
		{[]string{"/src/app"}, "/src/apple", false},
		{[]string{"/src/app/"}, "/src/app/web", true},
		{[]string{"/src/*/web"}, "/src/app/web", true},
		{[]string{"/src/*/web"}, "/src/app/web/x", false},
		{[]string{"/src/**"}, "/src/a/b", true},
		{[]string{"/other", "/src/app"}, "/src/app", true},
		{nil, "/src/app", false},
	}
	for _, c := range cases {
		if got := ScopeMatches(c.scopes, c.dir); got != c.want {
			t.Errorf("ScopeMatches(%q, %q) = %v, want %v", c.scopes, c.dir, got, c.want)
		}
	}
}

// A search from a directory reads the indexes scoped to it and the ambient
// globals; an opt-in global, or an index never given scopes, only when named.
func TestScopedFor(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	mk := func(name string, scopes []string, ambient bool) {
		st, err := reg.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if scopes != nil {
			if _, err := st.SetScopes(scopes); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.SetAmbient(ambient); err != nil {
			t.Fatal(err)
		}
	}
	mk("app__code", []string{"/src/app"}, false)
	mk("other__code", []string{"/src/other"}, false)
	mk("docs__handbook", []string{}, true)
	mk("site__crawl", []string{}, false)
	mk("old__notes", nil, false)

	got := reg.ScopedFor(context.Background(), "/src/app/web")
	if want := []string{"app__code", "docs__handbook"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ScopedFor = %q, want %q", got, want)
	}
	st, _ := reg.Get("old__notes")
	if _, set := st.Scopes(); set {
		t.Error("scopes never written must read as unset")
	}
	// Ambient is a global's mode: a scoped index is not read outside its scope.
	mk("app__code", []string{"/src/app"}, true)
	if got := reg.ScopedFor(context.Background(), "/elsewhere"); !reflect.DeepEqual(got, []string{"docs__handbook"}) {
		t.Errorf("outside every scope = %q, want only the ambient global", got)
	}
}
