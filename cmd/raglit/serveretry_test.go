package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// echoDaemon answers every request with its body, so a test can see that a
// retried POST still carried what was sent.
func echoDaemon() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})
}

func useTransport(t *testing.T, rt http.RoundTripper) {
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

// The restart case: the port is empty for a moment, then the same address
// answers again. The call waits and succeeds, and the POST body arrives.
func TestServeWaitsOutADaemonRestart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // the daemon is down

	go func() {
		time.Sleep(400 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		srv := &http.Server{Handler: echoDaemon()}
		t.Cleanup(func() { srv.Close() })
		srv.Serve(ln)
	}()

	rt := &restartTransport{next: http.DefaultTransport, wait: 5 * time.Second}
	useTransport(t, rt)
	b, err := daemonPostJSON("http://"+addr, "/ingest", map[string]string{"url": "a.pdf"})
	if err != nil {
		t.Fatalf("the call must outlast the restart: %v", err)
	}
	if !strings.Contains(string(b), "a.pdf") {
		t.Errorf("the retried POST lost its body: %q", b)
	}
	if !rt.restarted.Load() {
		t.Error("a call that waited must mark the restart, or no build check follows")
	}
}

// The daemon came back on another port: daemon.json names it, and a serve
// process whose address came from daemon.json follows it there.
func TestServeFollowsTheDaemonToANewAddress(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RAGLIT_ROOT", root)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	gone := ln.Addr().String()
	ln.Close()

	srv := httptest.NewServer(echoDaemon())
	defer srv.Close()
	if _, err := writeDaemonState(root, strings.TrimPrefix(srv.URL, "http://")); err != nil {
		t.Fatal(err)
	}

	rt := &restartTransport{next: http.DefaultTransport, wait: 5 * time.Second, follow: true, root: root}
	useTransport(t, rt)
	if _, err := daemonGet("http://"+gone, "/indexes", nil); err != nil {
		t.Fatalf("the call must find the daemon at its new address: %v", err)
	}

	// A daemon named on purpose (--daemon, config daemon_url) is not swapped
	// for whatever daemon.json says. Nothing answers, so the call fails.
	pinned := &restartTransport{next: http.DefaultTransport, wait: 300 * time.Millisecond, root: root}
	useTransport(t, pinned)
	if _, err := daemonGet("http://"+gone, "/indexes", nil); err == nil {
		t.Fatal("a pinned address must not follow daemon.json")
	}
}
