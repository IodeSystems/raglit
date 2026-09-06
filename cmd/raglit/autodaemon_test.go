package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iodesystems/raglit"
)

func healthServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
}

func TestDaemonHealthy(t *testing.T) {
	if daemonHealthy("http://127.0.0.1:1") {
		t.Fatal("a dead address must be unhealthy")
	}
	srv := healthServer(t)
	defer srv.Close()
	if !daemonHealthy(srv.URL) {
		t.Fatal("a live daemon must be healthy")
	}
}

// TestEnsureDaemon_ConnectsWhenHealthy: when a daemon is already up, ensureDaemon
// returns its URL WITHOUT auto-starting anything (no spawn side effects).
func TestEnsureDaemon_ConnectsWhenHealthy(t *testing.T) {
	srv := healthServer(t)
	defer srv.Close()
	homeOf := func() raglit.Home { return raglit.Home(t.TempDir()) }
	got, err := ensureDaemon(srv.URL, homeOf)
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	if got != srv.URL {
		t.Fatalf("ensureDaemon = %q, want %q (connect, not spawn)", got, srv.URL)
	}
}

// TestEnsureDaemon_RemoteDownErrors: a remote (non-loopback) target that's down
// is an error — we don't try to start a daemon on another host.
func TestEnsureDaemon_RemoteDownErrors(t *testing.T) {
	homeOf := func() raglit.Home { return raglit.Home(t.TempDir()) }
	if _, err := ensureDaemon("http://10.255.255.1:7420", homeOf); err == nil {
		t.Fatal("an unreachable remote daemon should error, not auto-start")
	}
}

func TestIsLoopback(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1", ""} {
		if !isLoopback(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"10.0.0.5", "example.com", "192.168.1.10"} {
		if isLoopback(h) {
			t.Errorf("%q should not be loopback", h)
		}
	}
}
