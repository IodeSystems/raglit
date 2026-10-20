package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Browser login for the review UI: OIDC authorization code + PKCE, then a
// session cookie.
//
// The SPA and attest UI fetch the API same-origin, so once the cookie is set
// every request they make carries it and nothing in the UI changes.
//
// Sessions live in memory: a daemon restart logs everyone out, which costs one
// redirect, and there is no session file to protect.
//
// CSRF: the cookie is SameSite=Lax (a cross-site POST does not carry it), and a
// state-changing request authenticated BY COOKIE must also come from this
// origin. A bearer-token request has no ambient credential and is not checked.

const (
	sessionCookie = "raglit_session"
	loginCookie   = "raglit_login"
	sessionTTL    = 12 * time.Hour
	loginTTL      = 10 * time.Minute
)

type webLogin struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	secure   bool // redirect URL is https → Secure cookies

	mu       sync.Mutex
	sessions map[string]session
	pending  map[string]pendingLogin // state → login in flight
}

type session struct {
	principal string
	expires   time.Time
}

type pendingLogin struct {
	verifier, nonce, next string
	expires               time.Time
}

func newWebLogin(p *oidc.Provider, clientID, redirectURL string) *webLogin {
	return &webLogin{
		oauth: oauth2.Config{
			ClientID: clientID, RedirectURL: redirectURL,
			Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: clientID}),
		secure:   strings.HasPrefix(redirectURL, "https://"),
		sessions: map[string]session{},
		pending:  map[string]pendingLogin{},
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on a working system
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// principal returns the session's principal, "" when there is no live session.
func (l *webLogin) principal(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.sessions[c.Value]
	if !ok || time.Now().After(s.expires) {
		delete(l.sessions, c.Value)
		return ""
	}
	return s.principal
}

// sameOrigin reports whether a cookie-authenticated request came from this
// daemon's own pages. Safe methods always pass.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// wantsPage reports whether a request is a browser navigation, which is
// redirected to login rather than answered 401.
func wantsPage(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (l *webLogin) cookie(name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: l.secure, SameSite: http.SameSiteLaxMode,
	}
}

// handle serves /auth/login, /auth/callback and /auth/logout. false = not an
// auth route.
func (l *webLogin) handle(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/auth/login":
		l.login(w, r)
	case "/auth/callback":
		l.callback(w, r)
	case "/auth/logout":
		if c, err := r.Cookie(sessionCookie); err == nil {
			l.mu.Lock()
			delete(l.sessions, c.Value)
			l.mu.Unlock()
		}
		http.SetCookie(w, l.cookie(sessionCookie, "", -time.Second))
		// A page, not a redirect to "/": "/" would send the browser straight
		// back through the IdP, whose own session logs it in again at once.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>raglit</title><p>Logged out. <a href="/auth/login">Log in again</a>.</p>`)
	default:
		return false
	}
	return true
}

func (l *webLogin) login(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	// Local paths only: an open redirect after login is a phishing aid.
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		next = "/"
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	l.mu.Lock()
	now := time.Now()
	for k, p := range l.pending {
		if now.After(p.expires) {
			delete(l.pending, k)
		}
	}
	l.pending[state] = pendingLogin{verifier: verifier, nonce: nonce, next: next, expires: now.Add(loginTTL)}
	l.mu.Unlock()
	// The state is also bound to THIS browser, so a callback URL handed to
	// someone else does not log them in as the person who started it.
	http.SetCookie(w, l.cookie(loginCookie, state, loginTTL))
	http.Redirect(w, r, l.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (l *webLogin) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	c, err := r.Cookie(loginCookie)
	if err != nil || state == "" || c.Value != state {
		http.Error(w, "login state does not match this browser; start again at /auth/login", http.StatusBadRequest)
		return
	}
	l.mu.Lock()
	p, ok := l.pending[state]
	delete(l.pending, state)
	l.mu.Unlock()
	if !ok || time.Now().After(p.expires) {
		http.Error(w, "login expired; start again at /auth/login", http.StatusBadRequest)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "login refused by the identity provider: "+e, http.StatusUnauthorized)
		return
	}
	tok, err := l.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(p.verifier))
	if err != nil {
		http.Error(w, "code exchange failed", http.StatusUnauthorized)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := l.verifier.Verify(r.Context(), raw)
	if err != nil || idt.Nonce != p.nonce || idt.Subject == "" {
		http.Error(w, "invalid id token", http.StatusUnauthorized)
		return
	}
	sid := randomToken()
	l.mu.Lock()
	l.sessions[sid] = session{principal: idt.Issuer + "#" + idt.Subject, expires: time.Now().Add(sessionTTL)}
	l.mu.Unlock()
	http.SetCookie(w, l.cookie(loginCookie, "", -time.Second))
	http.SetCookie(w, l.cookie(sessionCookie, sid, sessionTTL))
	http.Redirect(w, r, p.next, http.StatusFound)
}
