package main

// MCP client mode across a daemon restart (USER 2026-10-22). `raglit service
// restart`, a self-upgrade and a crash under systemd all leave a gap of a few
// seconds with nothing on the daemon's port. Before this, every tool call in
// that gap failed with "connection refused", and the agent saw a broken tool.
// Now the serve process waits the gap out, follows the daemon to a new address
// when daemon.json says it moved, and tells the agent when the daemon came back
// on a different build than this serve process.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/iodesystems/raglit"
)

// restartWait is how long a call waits for the daemon to come back. A restart
// is a 5s graceful shutdown plus startup; this covers that with room to spare.
const restartWait = 20 * time.Second

// restartTransport retries a request whose dial failed. A failed dial means
// nothing was sent, so a retry cannot run a request twice — a POST included.
type restartTransport struct {
	next   http.RoundTripper
	wait   time.Duration
	follow bool   // the address came from daemon.json: re-read it on a retry
	root   string // where daemon.json lives

	mu   sync.Mutex
	host string // the daemon's current host:port; "" = use the request's own

	// restarted is set when a call had to wait for the daemon. The tool
	// middleware clears it and checks the new daemon's build.
	restarted atomic.Bool
}

func newRestartTransport(base string) *restartTransport {
	t := &restartTransport{next: http.DefaultTransport, wait: restartWait, root: raglit.DefaultRoot()}
	// Follow daemon.json only when that is where the address came from. A
	// --daemon flag or a config daemon_url names a daemon on purpose.
	if base == defaultDaemonURL {
		t.follow = true
	} else if st, ok := readDaemonState(t.root); ok && "http://"+st.Addr == base {
		t.follow = true
	}
	return t
}

func (t *restartTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	deadline := time.Now().Add(t.wait)
	backoff := 100 * time.Millisecond
	for waited := false; ; waited = true {
		r, err := t.attempt(req)
		if err == nil {
			if waited {
				t.restarted.Store(true)
			}
			return r, nil
		}
		if !dialFailed(err) || time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-req.Context().Done():
			return nil, err
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Second)
		t.refollow()
	}
}

// attempt sends one copy of req to the daemon's current address. The copy
// gets a fresh body, because a failed dial may still have read the old one.
func (t *restartTransport) attempt(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	host := t.host
	t.mu.Unlock()
	r := req.Clone(req.Context())
	if req.Body != nil && req.GetBody != nil {
		b, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		r.Body = b
	}
	if host != "" {
		r.URL.Host = host
		r.Host = ""
	}
	return t.next.RoundTrip(r)
}

// refollow adopts the address in daemon.json when its daemon is alive. A
// daemon that restarted on another port is found there; a stale file from a
// dead daemon is ignored.
func (t *restartTransport) refollow() {
	if !t.follow {
		return
	}
	if st, ok := readDaemonState(t.root); ok && pidAlive(st.PID) {
		t.mu.Lock()
		t.host = st.Addr
		t.mu.Unlock()
	}
}

func dialFailed(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// restartNotes appends a note to a tool result after a restart brought up a
// daemon on a different build. This serve process keeps its own build until
// the MCP host restarts it, so tools added since then stay invisible to the
// agent until it reconnects. The startup check prints to stderr, which the
// agent never sees; the note goes in the result, which it does.
func restartNotes(t *restartTransport, base string) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := next(ctx, req)
			if res == nil || !t.restarted.CompareAndSwap(true, false) {
				return res, err
			}
			id, ok := probeDaemon(base)
			if !ok {
				return res, err
			}
			me := thisBuild
			if id.ExeHash != "" {
				me.ExeHash = exeHash()
			}
			if note := skewNote(me, id, base); note != "" {
				res.Content = append(res.Content, mcp.NewTextContent(note+
					"The raglit daemon restarted on a different build. Reconnect the raglit MCP server "+
					"(Claude Code: /mcp) to load its current tools."))
			}
			return res, err
		}
	}
}
