package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/iodesystems/raglit"
)

// Daemon authentication: the admin token, plus OIDC bearer tokens when an
// issuer is configured. Never open (USER 2026-10-30: "admin-token or oidc,
// never open"): with no issuer the daemon runs in admin-token mode.
//
// In the daemon rather than behind a proxy because the daemon binds a LIST of
// addresses (listenlist.go). A proxy that sets a trusted user header protects
// only the address it sits in front of; every other bind accepts the header from
// anyone. Validating the token here makes every bind the same.
//
// Browsers log in instead (authweb.go) when client_id + redirect_url are set.
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
	verifier   *oidc.IDTokenVerifier // nil = no issuer: admin-token mode
	web        *webLogin             // nil = no browser login (authweb.go)
	sessions   *sessionStore
	adminToken string // authsession.go
	admin      string // the principal the admin token acts as
	keys       *raglit.KeyStore
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
	a := &authenticator{
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.Audience}),
		sessions: newSessionStore(strings.HasPrefix(cfg.RedirectURL, "https://")),
	}
	if cfg.ClientID != "" || cfg.RedirectURL != "" {
		if cfg.ClientID == "" || cfg.RedirectURL == "" {
			return nil, fmt.Errorf("auth: browser login needs both client_id and redirect_url")
		}
		a.web = newWebLogin(p, cfg.ClientID, cfg.RedirectURL, a.sessions)
	}
	return a, nil
}

// principal validates a raw token and returns "<iss>#<sub>".
func (a *authenticator) principal(ctx context.Context, raw string) (string, error) {
	if a.verifier == nil {
		return "", fmt.Errorf("unknown token")
	}
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
		if a.handleAuth(w, r) || (a.web != nil && a.web.handle(w, r)) {
			return
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			if p := a.sessions.principal(r); p != "" {
				if !sameOrigin(r) {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r.WithContext(raglit.WithPrincipal(r.Context(), p)))
				return
			}
			if wantsPage(r) {
				if a.web != nil {
					http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				} else {
					signInPage(w, r)
				}
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="raglit"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if a.isAdminToken(strings.TrimSpace(raw)) {
			next.ServeHTTP(w, r.WithContext(raglit.WithPrincipal(r.Context(), a.admin)))
			return
		}
		// An API token or session key (keys.go).
		if tok := strings.TrimSpace(raw); strings.HasPrefix(tok, "rk_") && a.keys != nil {
			k, err := a.keys.Authenticate(tok, time.Now())
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="raglit", error="invalid_token"`)
				http.Error(w, "unknown, revoked or expired key", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(raglit.WithPrincipal(r.Context(), k.Principal)))
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

// daemonAuth turns on auth: always the admin token at <root>/admin.token, and
// OIDC when the config names an issuer. The ACL goes on with it where the
// storage is scoped (EnableACL refuses a single --home); a single home has one
// principal, the admin, so it needs none.
func daemonAuth(ctx context.Context, reg *raglit.Registry, cfg *raglit.AuthConfig, root string) (mw func(http.Handler) http.Handler, tokenPath string, err error) {
	a := &authenticator{sessions: newSessionStore(false)}
	admin := localAdmin
	if cfg != nil && cfg.Issuer != "" {
		if a, err = newAuthenticator(ctx, cfg); err != nil {
			return nil, "", err
		}
		// No configured admin: the admin token is the admin. Saves looking up
		// an OIDC subject before the first sign-in.
		if cfg.Admin != "" {
			admin = cfg.Admin
		}
	}
	if reg.Scoped() {
		if err := reg.EnableACL(admin); err != nil {
			return nil, "", fmt.Errorf("auth: %w", err)
		}
	}
	tokenPath = filepath.Join(root, adminTokenFile)
	if a.adminToken, err = ensureAdminToken(tokenPath); err != nil {
		return nil, "", fmt.Errorf("auth: admin token: %w", err)
	}
	a.admin = admin
	if a.keys, err = raglit.OpenKeyStore(filepath.Join(root, keysFile)); err != nil {
		return nil, "", fmt.Errorf("auth: keys: %w", err)
	}
	reg.SetKeyStore(a.keys)
	return a.middleware, tokenPath, nil
}
