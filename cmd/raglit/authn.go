package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/iodesystems/raglit"
)

// Daemon authentication: OIDC bearer tokens, validated in the daemon.
//
// In the daemon rather than behind a proxy because the daemon binds a LIST of
// addresses (listenlist.go). A proxy that sets a trusted user header protects
// only the address it sits in front of; every other bind accepts the header from
// anyone. Validating the token here makes every bind the same.
//
// Every request except /api/health needs a token — loopback included (USER
// decision 2026-10-19: one rule for every bind). Health stays open because every
// client probes it before routing, and it carries only build identity.
//
// The principal is "<iss>#<sub>". sub alone collides across issuers, and email
// changes. It is what acl.json records as an owner and what attest writes as
// Auth.

// authenticator validates bearer tokens against one issuer.
type authenticator struct {
	verifier *oidc.IDTokenVerifier
}

// newAuthenticator discovers the issuer (/.well-known/openid-configuration) and
// prepares JWKS validation. Keys are fetched and rotated by go-oidc.
//
// The token must be a JWT. Zitadel issues opaque access tokens unless the
// application (or machine user) is set to "JWT" access-token type.
func newAuthenticator(ctx context.Context, cfg *raglit.AuthConfig) (*authenticator, error) {
	if cfg.Audience == "" {
		return nil, fmt.Errorf("auth: audience is required with issuer %s", cfg.Issuer)
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: discover %s: %w", cfg.Issuer, err)
	}
	return &authenticator{verifier: p.Verifier(&oidc.Config{ClientID: cfg.Audience})}, nil
}

// principal validates a raw token and returns "<iss>#<sub>".
func (a *authenticator) principal(ctx context.Context, raw string) (string, error) {
	t, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return "", err
	}
	if t.Subject == "" {
		return "", fmt.Errorf("token has no subject")
	}
	return t.Issuer + "#" + t.Subject, nil
}

func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="raglit"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		p, err := a.principal(r.Context(), strings.TrimSpace(raw))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="raglit", error="invalid_token"`)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(raglit.WithPrincipal(r.Context(), p)))
	})
}

// daemonAuth turns on auth when the config names an issuer: the ACL on the
// registry, and a middleware for the handler. Nil middleware = auth off.
func daemonAuth(ctx context.Context, reg *raglit.Registry, cfg *raglit.AuthConfig) (func(http.Handler) http.Handler, error) {
	if cfg == nil || cfg.Issuer == "" {
		return nil, nil
	}
	if err := reg.EnableACL(cfg.Admin); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	a, err := newAuthenticator(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return a.middleware, nil
}
