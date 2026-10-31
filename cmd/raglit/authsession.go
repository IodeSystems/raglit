package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Browser sessions and the local admin token.
//
// A session is a cookie naming an in-memory principal. Two things start one:
// OIDC browser login (authweb.go) and the admin token (/auth/token). Both
// end at /auth/logout. Sessions live in memory: a daemon restart logs everyone
// out, which costs one sign-in, and there is no session file to protect.
//
// The admin token is the daemon's credential when no OIDC issuer is set, and
// the way to administer a fresh OIDC install before anyone has looked up their
// OIDC subject. It was OIDC-only (USER 2026-10-22); since USER 2026-10-30
// ("never open") it always exists. The daemon writes it to <root>/admin.token
// (0600) on its first start; whoever can read that file is the admin. `raglit service login-admin`
// reads it and opens /auth/admin#token=… — a fragment, which the browser never
// sends, so the token does not reach access logs. The page posts it to
// /auth/token and gets the ordinary session cookie, so nothing else in the UI
// changes. A CLI can send it as a bearer token too.

const adminTokenFile = "admin.token"

// localAdmin is the admin principal when the config names none: the admin
// token is then the only way to act as admin.
const localAdmin = "raglit#local-admin"

type sessionStore struct {
	secure bool // https deployment → Secure cookies

	mu sync.Mutex
	m  map[string]session
}

type session struct {
	principal string
	expires   time.Time
}

func newSessionStore(secure bool) *sessionStore {
	return &sessionStore{secure: secure, m: map[string]session{}}
}

func (s *sessionStore) cookie(name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	}
}

// start opens a session for principal and sets its cookie.
func (s *sessionStore) start(w http.ResponseWriter, principal string) {
	sid := randomToken()
	s.mu.Lock()
	s.m[sid] = session{principal: principal, expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	http.SetCookie(w, s.cookie(sessionCookie, sid, sessionTTL))
}

// principal returns the session's principal, "" when there is no live session.
func (s *sessionStore) principal(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.m[c.Value]
	if !ok || time.Now().After(ss.expires) {
		delete(s.m, c.Value)
		return ""
	}
	return ss.principal
}

func (s *sessionStore) end(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.m, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, s.cookie(sessionCookie, "", -time.Second))
}

// ensureAdminToken returns the admin token at path, writing a new one (0600)
// when there is none.
func ensureAdminToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	t := randomToken()
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

// isAdminToken compares in constant time.
func (a *authenticator) isAdminToken(t string) bool {
	return a.adminToken != "" && subtle.ConstantTimeCompare([]byte(t), []byte(a.adminToken)) == 1
}

// handleAuth serves /auth/admin, /auth/token and /auth/logout. false = not one
// of them.
func (a *authenticator) handleAuth(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/auth/admin":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, adminPage)
	case "/auth/token":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return true
		}
		// A token posted from another site would sign that browser in as admin
		// (login CSRF).
		if !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return true
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || !a.isAdminToken(strings.TrimSpace(body.Token)) {
			http.Error(w, "wrong admin token", http.StatusUnauthorized)
			return true
		}
		a.sessions.start(w, a.admin)
		w.WriteHeader(http.StatusNoContent)
	case "/auth/logout":
		a.sessions.end(w, r)
		// A page, not a redirect to "/": "/" would send the browser straight
		// back through the IdP, whose own session logs it in again at once.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		again := "Sign in again with <code>raglit service login-admin</code>."
		if a.web != nil {
			again = `<a href="/auth/login">Log in again</a>.`
		}
		fmt.Fprintf(w, `<!doctype html><title>raglit</title><p>Logged out. %s</p>`, again)
	default:
		return false
	}
	return true
}

// signInPage answers a browser navigation with no session when there is no
// OIDC browser login to redirect to: it names the command that signs in.
func signInPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>raglit — sign in</title>
<body style="font-family:system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;line-height:1.5">
<h1 style="font-size:1.25rem">Sign in to raglit</h1>
<p>On the machine that runs the daemon, run:</p>
<pre style="background:#8882;padding:.75rem;border-radius:6px">raglit service login-admin</pre>
<p>It opens this page signed in as admin. For a browser on another machine, add <code>--print --url http://%s</code> and open the URL it prints.</p>
</body>`, html.EscapeString(r.Host))
}

// adminPage exchanges #token=… for a session cookie, then opens the UI. The
// fragment is removed from the address bar and history before anything else.
const adminPage = `<!doctype html><meta charset="utf-8"><title>raglit — signing in</title>
<body style="font-family:system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;line-height:1.5">
<p id="msg">Signing in…</p>
<script>
(function () {
  var m = /(?:^#|&)token=([^&]+)/.exec(location.hash);
  history.replaceState(null, "", location.pathname + location.search);
  var msg = document.getElementById("msg");
  if (!m) { msg.textContent = "No admin token in the address. Run: raglit service login-admin"; return; }
  fetch("/auth/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: decodeURIComponent(m[1]) })
  }).then(function (r) {
    if (r.ok) { location.replace("/"); return; }
    msg.textContent = "That admin token was refused (" + r.status + "). Run raglit service login-admin again.";
  }, function (e) { msg.textContent = "Could not reach the daemon: " + e; });
})();
</script>
</body>`
