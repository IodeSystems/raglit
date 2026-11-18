package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/iodesystems/raglit/client"
)

// aclFixture is a daemon with a principal "dun" whose session keys wrote
// branches, and a second principal "other".
type aclFixture struct {
	base                     string
	dun, other, s1, s2, grnd string
}

func newACLFixture(t *testing.T) aclFixture {
	t.Helper()
	base, reg, _ := testDaemon(t)
	testIndex(t, reg) // t__default: admin-owned, private
	mint := func(as, body string) string {
		t.Helper()
		code, b := doAs(t, "POST", base+"/api/keys", as, body)
		if code != http.StatusOK {
			t.Fatalf("mint %s: %d %s", body, code, b)
		}
		var m struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
		return m.Token
	}
	upload := func(tok, index string) {
		t.Helper()
		if code, b := doAs(t, "POST", base+"/api/upload?index="+index, tok, `{"name":"x.md","text":"walrus"}`); code != http.StatusOK {
			t.Fatalf("upload to %s: %d %s", index, code, b)
		}
	}
	f := aclFixture{base: base}
	f.dun = mint(testToken, `{"principal":"dun"}`)
	f.other = mint(testToken, `{"principal":"other"}`)
	f.s1 = mint(f.dun, `{"project":"t","session":"s1"}`)
	f.s2 = mint(f.dun, `{"project":"t","session":"s2"}`)
	upload(f.s1, "t__sessions@s1")
	upload(f.s2, "t__sessions@s2")
	// A grandchild: s1's child key for a branch nothing has written, and
	// that child's own child, which writes it first and so owns it.
	child := mint(f.s1, `{"branch":"t__notes@s1"}`)
	f.grnd = mint(child, `{"branch":"t__notes@s1"}`)
	upload(f.grnd, "t__notes@s1")
	return f
}

func (f aclFixture) readable(t *testing.T, tok string) []string {
	t.Helper()
	code, b := doAs(t, "GET", f.base+"/api/status", tok, "")
	if code != http.StatusOK {
		t.Fatalf("/api/status: %d %s", code, b)
	}
	var lb liveBody
	if err := json.Unmarshal([]byte(b), &lb); err != nil {
		t.Fatal(err)
	}
	return liveNames(lb)
}

// A principal reads the indexes owned by the session keys it issued, at any
// depth, and nothing more: not the admin's private main index. Another
// principal reads none of them, and a session key does not read its
// sibling's branch.
func TestACL_PrincipalReadsItsKeysBranches(t *testing.T) {
	f := newACLFixture(t)
	dun := f.readable(t, f.dun)
	for _, want := range []string{"t__sessions@s1", "t__sessions@s2", "t__notes@s1"} {
		if !slices.Contains(dun, want) {
			t.Errorf("dun cannot read %s (its key's): %v", want, dun)
		}
	}
	if slices.Contains(dun, "t__default") {
		t.Errorf("dun reads the admin's private t__default: %v", dun)
	}
	for _, n := range f.readable(t, f.other) {
		if strings.Contains(n, "@") {
			t.Errorf("other reads dun's session branch %s", n)
		}
	}
	if s1 := f.readable(t, f.s1); slices.Contains(s1, "t__sessions@s2") {
		t.Errorf("session s1 reads its sibling's branch: %v", s1)
	}
	// Reading is not writing: dun may not write or delete its key's branch.
	if code, _ := doAs(t, "POST", f.base+"/api/upload?index=t__sessions@s1", f.dun, `{"name":"y.md","text":"x"}`); code != http.StatusForbidden {
		t.Errorf("dun writing its key's branch: %d, want 403", code)
	}
	if code, _ := doAs(t, "DELETE", f.base+"/api/branches?name=t__sessions@s1", f.dun, ""); code != http.StatusForbidden {
		t.Errorf("dun deleting its key's branch: %d, want 403", code)
	}
	if code, _ := doAs(t, "DELETE", f.base+"/indexes?name=t__sessions@s1", f.dun, ""); code != http.StatusForbidden {
		t.Errorf("dun deleting its key's index: %d, want 403", code)
	}
}

// `raglit status --json --all` with dun's token lists its sessions' branches.
func TestStatusCmd_JSONAllAsPrincipal(t *testing.T) {
	f := newACLFixture(t)
	old := http.DefaultTransport
	inner := old.(*client.Transport).Base
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+f.dun)
		return inner.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	out := captureStdout(t, func() {
		if err := runStatus([]string{"--daemon", f.base, "--home", t.TempDir(), "--json", "--all"}); err != nil {
			t.Fatal(err)
		}
	})
	var lb liveBody
	if err := json.Unmarshal([]byte(out), &lb); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	got := liveNames(lb)
	if !slices.Contains(got, "t__sessions@s1") || !slices.Contains(got, "t__notes@s1") || slices.Contains(got, "t__default") {
		t.Errorf("status --json --all as dun: %v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
