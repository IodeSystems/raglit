package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The session-key flow end to end, over the daemon's HTTP surface: the host
// mints a key for a session, the session writes its own branch and only that,
// mints a child for a sub-agent, and revoking the session's key revokes the
// child.
func TestKeys_SessionFlowOverHTTP(t *testing.T) {
	base, reg, _ := testDaemon(t)
	testIndex(t, reg)

	mint := func(token, body string) (int, map[string]any) {
		code, b := doAs(t, "POST", base+"/api/keys", token, body)
		var out map[string]any
		_ = json.Unmarshal([]byte(b), &out)
		return code, out
	}
	code, s1 := mint(testToken, `{"project":"t","session":"s1"}`)
	if code != http.StatusOK {
		t.Fatalf("mint session key: %d %v", code, s1)
	}
	tok := s1["token"].(string)
	if !strings.HasPrefix(tok, "rk_") {
		t.Fatalf("token = %q", tok)
	}

	up := `{"name":"fold-1.md","text":"walrus fold"}`
	if code, b := doAs(t, "POST", base+"/api/upload?index=t__sessions@s1", tok, up); code != http.StatusOK {
		t.Fatalf("session writing its branch: %d %s", code, b)
	}
	if code, _ := doAs(t, "POST", base+"/api/upload?index=t__default", tok, up); code != http.StatusForbidden {
		t.Fatalf("session writing main: %d, want 403", code)
	}
	if code, _ := doAs(t, "POST", base+"/api/upload?index=t__sessions@s2", tok, up); code != http.StatusForbidden {
		t.Fatalf("session writing another session's branch: %d, want 403", code)
	}
	// A branch selector of a session not yet written reads nothing, never 404.
	if code, b := doAs(t, "GET", base+"/search?q=walrus&index=t__sessions@s9", tok, ""); code != http.StatusOK {
		t.Fatalf("search of an unwritten branch: %d %s", code, b)
	}

	code, child := mint(tok, `{"branch":"t__sessions@s1"}`)
	if code != http.StatusOK {
		t.Fatalf("mint child key: %d %v", code, child)
	}
	ctok := child["token"].(string)
	if code, _ := mint(tok, `{"branch":"t__sessions@s2"}`); code != http.StatusForbidden {
		t.Fatalf("child for a branch outside the key's grants: %d, want 403", code)
	}
	if code, _ := doAs(t, "GET", base+"/indexes", ctok, ""); code != http.StatusOK {
		t.Fatalf("child key: %d", code)
	}
	id := s1["key"].(map[string]any)["id"].(string)
	if code, b := doAs(t, "POST", base+"/api/keys/revoke", testToken, `{"id":"`+id+`"}`); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, b)
	}
	if code, _ := doAs(t, "GET", base+"/indexes", ctok, ""); code != http.StatusUnauthorized {
		t.Fatalf("child of a revoked key: %d, want 401", code)
	}
	// Only the admin mints an API token.
	if code, _ := mint(ctok, `{"principal":"aw4"}`); code != http.StatusUnauthorized {
		t.Fatalf("revoked child minting: %d, want 401", code)
	}
}

// The aw4 bootstrap from the CLI: an API token written to a file, and the
// existing main index handed to its principal.
func TestKeys_APITokenBootstrapFromTheCLI(t *testing.T) {
	_, reg, flags := testDaemon(t)
	testIndex(t, reg)
	file := filepath.Join(t.TempDir(), "raglit.token")
	captureStdout(t, func() {
		if err := runKey(append([]string{"mint"}, append(flags, "--principal", "aw4", "--out", file)...)); err != nil {
			t.Fatal(err)
		}
	})
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v, want 0600", err, st)
	}
	b, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(b), "rk_") {
		t.Fatalf("token file holds %q", b)
	}
	out := captureStdout(t, func() {
		if err := runIndexCmd(append([]string{"default", "owner", "aw4"}, flags...)); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "now owned by aw4") || reg.ACLOf("t__default").Owner != "aw4" {
		t.Fatalf("owner: %s / %+v", out, reg.ACLOf("t__default"))
	}
}
