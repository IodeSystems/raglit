package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/iodesystems/raglit"
)

// fakeIssuer is an in-process OIDC issuer: discovery + JWKS, and a signer for
// tokens it issues.
type fakeIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]authReq
}

type authReq struct{ nonce, challenge, client string }

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fi := &fakeIssuer{key: key, codes: map[string]authReq{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                fi.srv.URL,
			"jwks_uri":                              fi.srv.URL + "/keys",
			"authorization_endpoint":                fi.srv.URL + "/auth",
			"token_endpoint":                        fi.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig",
		}}})
	})
	// Authorization endpoint: approve at once as user "web", bound to the
	// nonce and PKCE challenge the client sent.
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := "code-" + q.Get("state")
		fi.mu.Lock()
		fi.codes[code] = authReq{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), client: q.Get("client_id")}
		fi.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		fi.mu.Lock()
		ar, ok := fi.codes[r.Form.Get("code")]
		delete(fi.codes, r.Form.Get("code"))
		fi.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != ar.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "opaque", "token_type": "Bearer", "expires_in": 3600,
			"id_token": fi.idToken(t, "web", ar.client, ar.nonce),
		})
	})
	fi.srv = httptest.NewServer(mux)
	t.Cleanup(fi.srv.Close)
	return fi
}

func (fi *fakeIssuer) token(t *testing.T, sub, aud string, key *rsa.PrivateKey) string {
	t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(sig).Claims(jwt.Claims{
		Issuer: fi.srv.URL, Subject: sub, Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (fi *fakeIssuer) idToken(t *testing.T, sub, aud, nonce string) string {
	t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: fi.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(sig).Claims(jwt.Claims{
		Issuer: fi.srv.URL, Subject: sub, Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
	}).Claims(map[string]any{"nonce": nonce}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (fi *fakeIssuer) principal(sub string) string { return fi.srv.URL + "#" + sub }

// authTestServer is the daemon with auth on, over a scoped registry holding:
// alice's private index "a-priv", alice's public "a-pub", and a legacy index
// "legacy" with no acl.json (admin-owned).
func authTestServer(t *testing.T) (*httptest.Server, *fakeIssuer) {
	t.Helper()
	srv, fi, _ := authTestServerReg(t)
	return srv, fi
}

func authTestServerReg(t *testing.T) (*httptest.Server, *fakeIssuer, *raglit.Registry) {
	t.Helper()
	return authTestServerWith(t, false)
}

// authTestServerWith builds the test daemon; web turns on browser login with
// client id "raglit-web" and the server's own /auth/callback.
func authTestServerWith(t *testing.T, web bool) (*httptest.Server, *fakeIssuer, *raglit.Registry) {
	t.Helper()
	fi := newFakeIssuer(t)
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	legacy, _ := reg.Get("legacy")
	must(t, legacy.Ingest(ctx, raglit.Document{Path: "file:///l.md", Title: "L",
		Fragments: []raglit.Fragment{{Text: "walrus legacy"}}}))

	cfg := &raglit.AuthConfig{Issuer: fi.srv.URL, Audience: "raglit", Admin: fi.principal("admin")}
	if web {
		cfg.ClientID, cfg.RedirectURL = "raglit-web", srv.URL+"/auth/callback"
	}
	mw, err := daemonAuth(ctx, reg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice := raglit.WithPrincipal(ctx, fi.principal("alice"))
	for name, vis := range map[string]raglit.Visibility{"a-priv": raglit.Private, "a-pub": raglit.Public} {
		st, err := reg.Write(alice, name)
		if err != nil {
			t.Fatal(err)
		}
		must(t, st.Ingest(ctx, raglit.Document{Path: "file:///" + name + ".md", Title: name,
			Fragments: []raglit.Fragment{{Text: "walrus " + name}}}))
		must(t, reg.SetVisibility(alice, name, vis))
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(raglit.Home(root))
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, nil, raglit.GCPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = mw(h)
	t.Cleanup(func() { srv.Close(); reg.Close() })
	return srv, fi, reg
}

// drainAll runs every queued job in every index (the tests start no workers).
func drainAll(t *testing.T, reg *raglit.Registry) {
	t.Helper()
	for _, n := range reg.Names() {
		st, err := reg.Existing(n)
		if err != nil {
			continue
		}
		w := &raglit.Worker{Store: st}
		for range 50 {
			did, err := w.ProcessOne(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !did {
				break
			}
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func doAs(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Every surface refuses an unauthenticated request — the huma API, GraphQL,
// the plain routes and the SPA — except the health probe.
func TestAuth_EverySurfaceNeedsAToken(t *testing.T) {
	srv, fi := authTestServer(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/indexes", ""},
		{"GET", "/search?q=walrus", ""},
		{"POST", "/graphql", `{"query":"{__typename}"}`},
		{"GET", "/api/page-image?index=a-pub&path=x", ""},
		{"GET", "/", ""},
		{"GET", "/i/a-pub/d/x", ""},
		{"GET", "/openapi.json", ""},
	} {
		if code, _ := doAs(t, c.method, srv.URL+c.path, "", c.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", c.method, c.path, code)
		}
	}
	if code, _ := doAs(t, "GET", srv.URL+"/api/health", "", ""); code != http.StatusOK {
		t.Errorf("health without a token: %d, want 200", code)
	}

	// Wrong signing key, wrong audience: both refused.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	for name, tok := range map[string]string{
		"forged":    fi.token(t, "alice", "raglit", other),
		"wrong aud": fi.token(t, "alice", "someone-else", fi.key),
		"garbage":   "not.a.jwt",
	} {
		if code, _ := doAs(t, "GET", srv.URL+"/indexes", tok, ""); code != http.StatusUnauthorized {
			t.Errorf("%s token: %d, want 401", name, code)
		}
	}
}

// Search enforces visibility in the daemon: bob sees alice's public index only.
func TestAuth_SearchHonorsVisibility(t *testing.T) {
	srv, fi := authTestServer(t)
	alice, bob, admin := fi.token(t, "alice", "raglit", fi.key), fi.token(t, "bob", "raglit", fi.key), fi.token(t, "admin", "raglit", fi.key)

	_, body := doAs(t, "GET", srv.URL+"/search?q=walrus", alice, "")
	if !strings.Contains(body, "a-priv.md") || !strings.Contains(body, "a-pub.md") || strings.Contains(body, "l.md") {
		t.Errorf("alice search: want a-priv + a-pub, not legacy:\n%s", clip(body, 600))
	}
	_, body = doAs(t, "GET", srv.URL+"/search?q=walrus", bob, "")
	if strings.Contains(body, "a-priv") || strings.Contains(body, "l.md") || !strings.Contains(body, "a-pub.md") {
		t.Errorf("bob search: want only a-pub:\n%s", clip(body, 600))
	}
	// Naming the private index explicitly does not get around the filter.
	_, body = doAs(t, "GET", srv.URL+"/search?q=walrus&index=a-priv", bob, "")
	if strings.Contains(body, "a-priv.md") {
		t.Errorf("bob searching a-priv by name got its docs:\n%s", clip(body, 600))
	}
	_, body = doAs(t, "GET", srv.URL+"/search?q=walrus", admin, "")
	if !strings.Contains(body, "l.md") || strings.Contains(body, "a-priv") {
		t.Errorf("admin search: want legacy + a-pub, not alice's private:\n%s", clip(body, 600))
	}

	_, body = doAs(t, "GET", srv.URL+"/indexes", bob, "")
	if strings.Contains(body, "a-priv") || strings.Contains(body, "legacy") {
		t.Errorf("bob's index list leaks:\n%s", clip(body, 600))
	}
	if code, _ := doAs(t, "GET", srv.URL+"/api/documents?index=a-priv", bob, ""); code != http.StatusNotFound {
		t.Errorf("bob listing a-priv documents: %d, want 404", code)
	}
}

// Writes are the owner's; host-filesystem operations are the admin's.
func TestAuth_WritesAndHostOperations(t *testing.T) {
	srv, fi := authTestServer(t)
	alice, bob, admin := fi.token(t, "alice", "raglit", fi.key), fi.token(t, "bob", "raglit", fi.key), fi.token(t, "admin", "raglit", fi.key)

	if code, b := doAs(t, "POST", srv.URL+"/api/notes?index=a-pub&path=file:///a-pub.md", bob, `{"author":"bob","body":"hi"}`); code != http.StatusForbidden {
		t.Errorf("bob noting on alice's public index: %d, want 403\n%s", code, clip(b, 300))
	}
	if code, b := doAs(t, "POST", srv.URL+"/api/notes?index=a-priv&path=file:///a-priv.md", bob, `{"author":"bob","body":"hi"}`); code != http.StatusNotFound {
		t.Errorf("bob noting on alice's private index: %d, want 404\n%s", code, clip(b, 300))
	}
	if code, b := doAs(t, "POST", srv.URL+"/ingest", alice, `{"targets":["/etc/hostname"],"index":"a-priv"}`); code != http.StatusForbidden {
		t.Errorf("alice ingesting a host path: %d, want 403\n%s", code, clip(b, 300))
	}
	if code, b := doAs(t, "POST", srv.URL+"/api/ocr", alice, `{"path":"/etc/hostname"}`); code != http.StatusForbidden {
		t.Errorf("alice OCR of a host path: %d, want 403\n%s", code, clip(b, 300))
	}
	if code, _ := doAs(t, "GET", srv.URL+"/api/tools", alice, ""); code != http.StatusForbidden {
		t.Errorf("alice reading the daemon environment: %d, want 403", code)
	}
	if code, _ := doAs(t, "GET", srv.URL+"/api/tools", admin, ""); code != http.StatusOK {
		t.Errorf("admin reading the daemon environment: %d, want 200", code)
	}

	// Fork: bob may fork the public index and owns the fork; not the private one.
	if code, b := doAs(t, "POST", srv.URL+"/api/branches", bob, `{"name":"b-fork","parent":"a-pub"}`); code != http.StatusOK {
		t.Errorf("bob forking a-pub: %d\n%s", code, clip(b, 300))
	}
	if code, _ := doAs(t, "POST", srv.URL+"/api/branches", bob, `{"name":"b-fork2","parent":"a-priv"}`); code != http.StatusNotFound {
		t.Errorf("bob forking a-priv: %d, want 404", code)
	}
	_, body := doAs(t, "GET", srv.URL+"/api/branches", alice, "")
	if strings.Contains(body, "b-fork") {
		t.Errorf("alice sees bob's private fork:\n%s", clip(body, 300))
	}
	if code, _ := doAs(t, "DELETE", srv.URL+"/indexes?name=a-pub", bob, ""); code != http.StatusForbidden {
		t.Errorf("bob deleting a-pub: %d, want 403", code)
	}
}

// GraphQL dispatches in-process; the principal must ride the context through it.
func TestAuth_GraphQLCarriesThePrincipal(t *testing.T) {
	srv, fi := authTestServer(t)
	q := `{"query":"{ raglit { search(q: \"walrus\") { hits { docId } } } }"}`
	_, body := doAs(t, "POST", srv.URL+"/graphql", fi.token(t, "bob", "raglit", fi.key), q)
	if !strings.Contains(body, "a-pub.md") {
		t.Fatalf("bob's GraphQL search found nothing; the principal did not reach the handler:\n%s", clip(body, 600))
	}
	if strings.Contains(body, "a-priv") {
		t.Fatalf("bob's GraphQL search leaks a-priv:\n%s", clip(body, 600))
	}
}

// A non-admin adds documents by content; the owner rule still holds.
func TestAuth_UploadIsOwnerWrite(t *testing.T) {
	srv, fi := authTestServer(t)
	alice, bob := fi.token(t, "alice", "raglit", fi.key), fi.token(t, "bob", "raglit", fi.key)
	code, body := doAs(t, "POST", srv.URL+"/api/upload?index=b-mem", bob, `{"name":"m/one.md","text":"bob remembers the narwhal"}`)
	if code != http.StatusOK || !strings.Contains(body, `"job_id"`) {
		t.Fatalf("bob uploading to a new index: %d\n%s", code, clip(body, 400))
	}
	if code, _ := doAs(t, "POST", srv.URL+"/api/upload?index=a-pub", bob, `{"name":"x.md","text":"x"}`); code != http.StatusForbidden {
		t.Errorf("bob uploading to alice's public index: %d, want 403", code)
	}
	if code, _ := doAs(t, "POST", srv.URL+"/api/upload?index=b-mem", bob, `{"name":"../../escape.md","text":"x"}`); code != http.StatusBadRequest {
		t.Errorf("escaping upload name: %d, want 400", code)
	}
	// The new index is bob's and private.
	if code, _ := doAs(t, "GET", srv.URL+"/api/jobs?index=b-mem", alice, ""); code != http.StatusNotFound {
		t.Errorf("alice reading bob's new index: %d, want 404", code)
	}
}

// dun's flow over HTTP: two people register one project id; each note goes to
// the private or the public layer; a branch search fans out to the siblings
// the caller can read, and a whole-namespace search counts the base once.
func TestAuth_RegisterUploadSearch(t *testing.T) {
	srv, fi, reg := authTestServerReg(t)
	alice, bob := fi.token(t, "alice", "raglit", fi.key), fi.token(t, "bob", "raglit", fi.key)
	register := func(tok string) raglit.Registration {
		t.Helper()
		code, body := doAs(t, "POST", srv.URL+"/api/projects/register", tok, `{"project_id":"rootsha","label":"github.com/x/y"}`)
		if code != http.StatusOK {
			t.Fatalf("register: %d\n%s", code, clip(body, 400))
		}
		var r raglit.Registration
		must(t, json.Unmarshal([]byte(body), &r))
		return r
	}
	ra, rb := register(alice), register(bob)
	if ra.Base != rb.Base || ra.Private == rb.Private || ra.Namespace != rb.Namespace {
		t.Fatalf("registrations %+v / %+v", ra, rb)
	}
	upload := func(tok, index, name, text string) {
		t.Helper()
		code, body := doAs(t, "POST", srv.URL+"/api/upload?index="+index, tok, `{"name":"`+name+`","text":"`+text+`"}`)
		if code != http.StatusOK {
			t.Fatalf("upload: %d\n%s", code, clip(body, 400))
		}
	}
	upload(alice, ra.Private, "secret.md", "alice secret narwhal")
	upload(alice, ra.Public, "shared.md", "alice shared narwhal")
	// The base belongs to its first registrant until a maintainer takes it.
	upload(alice, ra.Base, "base.md", "base narwhal fact")
	if code, _ := doAs(t, "POST", srv.URL+"/api/upload?index="+ra.Private, bob, `{"name":"x.md","text":"x"}`); code != http.StatusNotFound {
		t.Errorf("bob uploading into alice's private layer: %d, want 404", code)
	}
	drainAll(t, reg)

	search := func(tok, query string) string {
		_, body := doAs(t, "GET", srv.URL+"/search?q=narwhal&n=20&"+query, tok, "")
		return body
	}
	b := search(bob, "index="+rb.Private)
	if !strings.Contains(b, "shared.md") || !strings.Contains(b, "base.md") || strings.Contains(b, "secret.md") {
		t.Fatalf("bob's layer search: want alice's public note + base, not her private note:\n%s", clip(b, 800))
	}
	if b := search(bob, "index="+rb.Private+"&siblings=false"); strings.Contains(b, "shared.md") || !strings.Contains(b, "base.md") {
		t.Fatalf("siblings=false: want base only:\n%s", clip(b, 800))
	}
	if b := search(alice, "index="+ra.Private); !strings.Contains(b, "secret.md") || !strings.Contains(b, "shared.md") {
		t.Fatalf("alice's private layer search: want both of her notes:\n%s", clip(b, 800))
	}
	// The daemon MCP's selector for a project: every readable index, base once.
	b = search(bob, "index="+ra.Namespace+"__*")
	if strings.Count(b, `"title":"base.md"`) != 1 || strings.Contains(b, "secret.md") || !strings.Contains(b, "shared.md") {
		t.Fatalf("namespace search: want base once, alice's public note, not her private one:\n%s", clip(b, 1200))
	}
	_, body := doAs(t, "GET", srv.URL+"/api/projects/lookup?id=rootsha", bob, "")
	if !strings.Contains(body, ra.Public) || strings.Contains(body, ra.Private) || !strings.Contains(body, rb.Private) {
		t.Fatalf("lookup for bob: %s", clip(body, 400))
	}
}

// A browser logs in through the IdP and then uses the UI and API by cookie; a
// cross-site write with that cookie is refused.
func TestAuth_BrowserLogin(t *testing.T) {
	srv, fi, reg := authTestServerWith(t, true)
	// The browser user owns an index, so a write by cookie has something to hit.
	if _, err := reg.Write(raglit.WithPrincipal(context.Background(), fi.principal("web")), "w-own"); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}

	get := func(path string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Accept", "text/html")
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	// Navigation → login → IdP → callback → back to the page asked for.
	resp, _ := get("/indexes")
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/indexes" {
		t.Fatalf("after login: %d at %s, want 200 at /indexes", resp.StatusCode, resp.Request.URL)
	}
	_, body := get("/indexes")
	if !strings.Contains(body, "w-own") || strings.Contains(body, "a-priv") {
		t.Fatalf("the browser session is not the web user's:\n%s", clip(body, 400))
	}

	post := func(origin string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/visibility", strings.NewReader(`{"index":"w-own","visibility":"public"}`))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-origin write by cookie: %d, want 403", code)
	}
	if code := post(""); code != http.StatusForbidden {
		t.Errorf("write by cookie with no Origin: %d, want 403", code)
	}
	if code := post(srv.URL); code != http.StatusOK {
		t.Errorf("same-origin write by cookie: %d, want 200", code)
	}

	// A callback whose state was not started in this browser is refused.
	other := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r2, err := other.Get(srv.URL + "/auth/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("foreign callback: %d, want 400", r2.StatusCode)
	}
	// An open redirect through next= is not followed off-site.
	r3, err := other.Get(srv.URL + "/auth/login?next=//evil.example/x")
	if err != nil {
		t.Fatal(err)
	}
	r3.Body.Close()
	if u, _ := url.Parse(r3.Header.Get("Location")); u == nil || !strings.HasPrefix(u.String(), fi.srv.URL) {
		t.Fatalf("login redirect = %q", r3.Header.Get("Location"))
	}

	// Logout ends the session.
	get("/auth/logout")
	req, _ := http.NewRequest("GET", srv.URL+"/indexes", nil)
	resp3, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("API after logout: %d, want 401", resp3.StatusCode)
	}
}

// A maintainer takes over a project base: the owner hands it over, after which
// only the maintainer writes to it.
func TestAuth_TransferOwnership(t *testing.T) {
	srv, fi := authTestServer(t)
	alice, bob, maint := fi.token(t, "alice", "raglit", fi.key), fi.token(t, "bob", "raglit", fi.key), fi.token(t, "maint", "raglit", fi.key)
	body := `{"index":"a-pub","owner":"` + fi.principal("maint") + `"}`
	if code, _ := doAs(t, "POST", srv.URL+"/api/owner", bob, body); code != http.StatusForbidden {
		t.Errorf("bob transferring alice's index: %d, want 403", code)
	}
	if code, b := doAs(t, "POST", srv.URL+"/api/owner", alice, body); code != http.StatusOK {
		t.Fatalf("alice transferring her index: %d %s", code, clip(b, 300))
	}
	up := `{"name":"m.md","text":"maintained"}`
	if code, _ := doAs(t, "POST", srv.URL+"/api/upload?index=a-pub", alice, up); code != http.StatusForbidden {
		t.Errorf("former owner writing: %d, want 403", code)
	}
	if code, b := doAs(t, "POST", srv.URL+"/api/upload?index=a-pub", maint, up); code != http.StatusOK {
		t.Errorf("new owner writing: %d %s", code, clip(b, 300))
	}
}
