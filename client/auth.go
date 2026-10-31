package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Bearer tokens for a daemon that runs with OIDC auth.
//
// Two sources, in order:
//   - $RAGLIT_TOKEN: a token, used as is. It cannot be renewed; when it expires
//     the daemon answers 401 and the caller sees that.
//   - a token command ($RAGLIT_TOKEN_COMMAND, else config auth.token_command):
//     run through `sh -c`, prints a token on stdout. Run on first use and again
//     after a 401, so an expired token is replaced without the caller noticing.
//     A Zitadel machine user's client-credentials exchange, or a CLI that prints
//     a person's cached token, goes here.
//
// With neither, a daemon on THIS machine (a loopback address) is asked where
// its admin token file is (/api/health names it, and health is the one open
// route) and the file is read. Whoever can read that file is the daemon's
// admin, so on a single-user machine no setup is needed. It is never done for
// a remote daemon: a remote health answer naming a local path would make the
// client send that file to it.
//
// raglit never stores a token on disk itself; the daemon writes its own admin
// token.

// TokenSource yields the bearer token. The zero value and nil have no token.
type TokenSource struct {
	static string
	cmd    string
	local  bool // the admin token file a loopback daemon names

	mu     sync.Mutex
	cached string
}

// EnvTokenSource reads $RAGLIT_TOKEN and $RAGLIT_TOKEN_COMMAND, falling back to
// configCmd (config auth.token_command). Nil when none is set.
func EnvTokenSource(configCmd string) *TokenSource {
	if t := strings.TrimSpace(os.Getenv("RAGLIT_TOKEN")); t != "" {
		return &TokenSource{static: t}
	}
	if c := strings.TrimSpace(os.Getenv("RAGLIT_TOKEN_COMMAND")); c != "" {
		return &TokenSource{cmd: c}
	}
	if c := strings.TrimSpace(configCmd); c != "" {
		return &TokenSource{cmd: c}
	}
	return nil
}

// Token returns the current token; renew discards a cached one and runs the
// command again. A static token is returned as is either way.
func (s *TokenSource) Token(renew bool) (string, error) {
	if s.static != "" {
		return s.static, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" && !renew {
		return s.cached, nil
	}
	var stderr bytes.Buffer
	c := exec.Command("sh", "-c", s.cmd)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("raglit token command: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return "", fmt.Errorf("raglit token command printed no token")
	}
	s.cached = t
	return t, nil
}

func (s *TokenSource) renewable() bool { return s.cmd != "" || s.local }

// LocalAdminTokenSource reads the admin token of a daemon on this machine.
func LocalAdminTokenSource() *TokenSource { return &TokenSource{local: true} }

// localAdminToken asks the daemon at base (loopback only) where its admin
// token is, and reads it. base is "scheme://host".
func localAdminToken(rt http.RoundTripper, base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || !loopback(u.Hostname()) {
		return "", fmt.Errorf("raglit: daemon %s is not on this machine: set $RAGLIT_TOKEN or $RAGLIT_TOKEN_COMMAND", base)
	}
	hc := &http.Client{Transport: rt, Timeout: 5 * time.Second}
	resp, err := hc.Get(base + "/api/health")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var h struct {
		AdminToken string `json:"admin_token"`
	}
	// A daemon that names no token file is older than the never-open rule (or
	// a test double): send nothing, as before, and let it answer.
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h) != nil || h.AdminToken == "" {
		return "", nil
	}
	b, err := os.ReadFile(h.AdminToken)
	if err != nil {
		return "", fmt.Errorf("raglit: read the daemon's admin token: %w (run as the daemon's user, or set $RAGLIT_TOKEN)", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("raglit: %s is empty", h.AdminToken)
	}
	return t, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Transport adds the bearer token to requests for the hosts Allow accepts, and
// retries once with a renewed token after a 401. Every other request passes
// through untouched — a daemon token must never reach a model endpoint.
type Transport struct {
	Base  http.RoundTripper
	Src   *TokenSource
	Allow func(host string) bool
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Src == nil || t.Allow == nil || !t.Allow(req.URL.Host) {
		return t.base().RoundTrip(req)
	}
	base := req.URL.Scheme + "://" + req.URL.Host
	tok, err := t.token(base, false)
	if err != nil {
		return nil, err
	}
	if tok == "" { // a local daemon with no token file: nothing to send
		return t.base().RoundTrip(req)
	}
	resp, err := t.base().RoundTrip(withBearer(req, tok))
	if err != nil || resp.StatusCode != http.StatusUnauthorized || !t.Src.renewable() {
		return resp, err
	}
	// Retry only when the body can be sent again.
	if req.Body != nil && req.GetBody == nil {
		return resp, nil
	}
	resp.Body.Close()
	tok, err = t.token(base, true)
	if err != nil {
		return nil, err
	}
	again := req
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		again = req.Clone(req.Context())
		again.Body = body
	}
	return t.base().RoundTrip(withBearer(again, tok))
}

// token is the source's token for the daemon at base. A local source reads
// the file each time it is asked to renew, so a rotated admin token is picked
// up after one 401; it caches per process otherwise.
func (t *Transport) token(base string, renew bool) (string, error) {
	if !t.Src.local {
		return t.Src.Token(renew)
	}
	t.Src.mu.Lock()
	defer t.Src.mu.Unlock()
	if t.Src.cached != "" && !renew {
		return t.Src.cached, nil
	}
	tok, err := localAdminToken(t.base(), base)
	if err != nil {
		return "", err
	}
	t.Src.cached = tok // "" is not cached: the next request asks again
	return tok, nil
}

func withBearer(req *http.Request, tok string) *http.Request {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}
