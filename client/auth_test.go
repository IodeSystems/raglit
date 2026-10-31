package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The token goes to the daemon only, and a 401 renews it once through the
// command.
func TestTransport_RenewsOnceAndOnlyForTheDaemon(t *testing.T) {
	var seen []string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer tok-2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer daemon.Close()
	var other string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other = r.Header.Get("Authorization")
	}))
	defer model.Close()

	// The command prints tok-1, then tok-2: a counter file stands in for an IdP
	// that issues a fresh token per exchange.
	n := filepath.Join(t.TempDir(), "n")
	cmd := fmt.Sprintf(`c=$(cat %[1]s 2>/dev/null || echo 0); c=$((c+1)); echo $c > %[1]s; echo tok-$c`, n)
	src := &TokenSource{cmd: cmd}
	du, _ := url.Parse(daemon.URL)
	hc := &http.Client{Transport: &Transport{Src: src, Allow: func(h string) bool { return h == du.Host }}}

	resp, err := hc.Post(daemon.URL, "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(seen) != 2 || seen[0] != "Bearer tok-1" {
		t.Fatalf("status %d, daemon saw %v; want 200 after one renewal", resp.StatusCode, seen)
	}
	if _, err := hc.Get(model.URL); err != nil {
		t.Fatal(err)
	}
	if other != "" {
		t.Fatalf("the daemon token reached another host: %q", other)
	}
}

func TestEnvTokenSource_Order(t *testing.T) {
	t.Setenv("RAGLIT_TOKEN", "")
	t.Setenv("RAGLIT_TOKEN_COMMAND", "")
	if EnvTokenSource("") != nil {
		t.Fatal("no source configured, got one")
	}
	if s := EnvTokenSource("echo cfg"); s == nil || s.cmd != "echo cfg" {
		t.Fatalf("config command: %+v", s)
	}
	t.Setenv("RAGLIT_TOKEN_COMMAND", "echo env")
	if s := EnvTokenSource("echo cfg"); s.cmd != "echo env" {
		t.Fatalf("env command should win over config: %+v", s)
	}
	t.Setenv("RAGLIT_TOKEN", "static")
	if tok, _ := EnvTokenSource("echo cfg").Token(true); tok != "static" {
		t.Fatalf("RAGLIT_TOKEN should win: %q", tok)
	}
}

// daemonDouble serves h as a daemon too old to name an admin token file, so a
// client sends no token, as it did before the never-open rule.
func daemonDouble(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		h(w, r)
	}))
}

// A daemon on this machine names its admin token file, and a client with no
// token configured reads it: the daemon is never open, and a single-user
// machine still needs no setup.
func TestLocalDaemonsAdminTokenIsReadFromTheFileItNames(t *testing.T) {
	t.Setenv("RAGLIT_TOKEN", "")
	t.Setenv("RAGLIT_TOKEN_COMMAND", "")
	file := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			if r.Header.Get("Authorization") != "" {
				t.Error("health was sent a token")
			}
			_, _ = w.Write([]byte(`{"status":"ok","auth":true,"admin_token":` + strconv.Quote(file) + `}`))
			return
		}
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"documents":[]}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).Documents(context.Background(), "i"); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q, want the token from %s", got, file)
	}
}

// A remote daemon is never sent a local file: its health answer could name
// any path on this machine.
func TestARemoteDaemonIsNotSentALocalAdminToken(t *testing.T) {
	_, err := localAdminToken(http.DefaultTransport, "http://192.0.2.1:7420")
	if err == nil {
		t.Fatal("a non-loopback daemon got a local admin token")
	}
}
