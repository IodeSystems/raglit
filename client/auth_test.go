package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
