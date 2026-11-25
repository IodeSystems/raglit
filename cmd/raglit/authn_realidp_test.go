package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// TestAuth_RealIdP verifies a JWT access token from a real Zitadel against the
// daemon's authenticator. Skipped unless RAGLIT_REALIDP_ENV names an env file
// (ZITADEL_ISSUER, ZITADEL_TEST_CLIENT_ID, ZITADEL_TEST_CLIENT_SECRET,
// ZITADEL_TEST_AUDIENCE): a machine user with JWT access tokens, via client
// credentials. The audience is a Zitadel project id.
func TestAuth_RealIdP(t *testing.T) {
	path := os.Getenv("RAGLIT_REALIDP_ENV")
	if path == "" {
		t.Skip("RAGLIT_REALIDP_ENV not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, l := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(k, "#") {
			env[k] = strings.TrimSpace(v)
		}
	}
	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {"openid urn:zitadel:iam:org:project:id:" + env["ZITADEL_TEST_AUDIENCE"] + ":aud"},
	}
	req, _ := http.NewRequest("POST", env["ZITADEL_ISSUER"]+"/oauth/v2/token", strings.NewReader(form.Encode()))
	req.SetBasicAuth(url.QueryEscape(env["ZITADEL_TEST_CLIENT_ID"]), url.QueryEscape(env["ZITADEL_TEST_CLIENT_SECRET"]))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	if tok.AccessToken == "" {
		t.Fatalf("token request: status %d error %q", resp.StatusCode, tok.Error)
	}
	ctx := context.Background()
	a, err := newAuthenticator(ctx, &raglit.AuthConfig{Issuer: env["ZITADEL_ISSUER"], Audience: env["ZITADEL_TEST_AUDIENCE"]})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.principal(ctx, tok.AccessToken)
	if err != nil {
		t.Fatalf("real token refused: %v", err)
	}
	if want := env["ZITADEL_ISSUER"] + "#" + env["ZITADEL_TEST_USER_ID"]; p != want {
		t.Fatalf("principal %q, want %q", p, want)
	}
	if _, err := a.principal(ctx, tok.AccessToken+"x"); err == nil {
		t.Fatal("tampered token accepted")
	}
	b, _ := newAuthenticator(ctx, &raglit.AuthConfig{Issuer: env["ZITADEL_ISSUER"], Audience: "000"})
	if _, err := b.principal(ctx, tok.AccessToken); err == nil {
		t.Fatal("wrong audience accepted")
	}
}
