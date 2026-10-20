package client

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
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
// raglit never stores a token on disk itself.

// TokenSource yields the bearer token. The zero value and nil have no token.
type TokenSource struct {
	static string
	cmd    string

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

func (s *TokenSource) renewable() bool { return s.cmd != "" }

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
	tok, err := t.Src.Token(false)
	if err != nil {
		return nil, err
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
	tok, err = t.Src.Token(true)
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

func withBearer(req *http.Request, tok string) *http.Request {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}
