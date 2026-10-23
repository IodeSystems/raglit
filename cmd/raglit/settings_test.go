package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// settingsServer is the daemon handler over a home whose config.json is raw.
func settingsServer(t *testing.T, raw string) (*httptest.Server, raglit.Home) {
	t.Helper()
	home := raglit.Home(t.TempDir())
	must(t, home.Ensure())
	must(t, os.WriteFile(home.ConfigPath(), []byte(raw), 0o600))
	reg, err := raglit.OpenRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(home)
	h, err := buildGatHandler(reg, lf, home, 8, nil, raglit.GCPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); reg.Close() })
	reloads := 0
	old := requestReload
	requestReload = func() { reloads++ }
	t.Cleanup(func() { requestReload = old })
	return srv, home
}

// A pre-provider config shows its endpoint as provider "default"; a save
// writes providers and roles, keeps keys it was not given, keeps every config
// key it does not own, and never returns a key.
func TestModelSettings_RoundTrip(t *testing.T) {
	srv, home := settingsServer(t, `{"base_url":"http://legacy/v1","api_key":"legacy-secret",
		"vision_model":"v1","embed_model":"e1","ocr":{"cheap_engine":"tesseract"},"project":"p"}`)

	code, body := doAs(t, "GET", srv.URL+"/api/settings/models", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET: %d %s", code, body)
	}
	if strings.Contains(body, "legacy-secret") {
		t.Fatal("GET returned a key")
	}
	var got struct {
		Providers []providerView      `json:"providers"`
		Roles     map[string]roleView `json:"roles"`
	}
	must(t, json.Unmarshal([]byte(body), &got))
	if len(got.Providers) != 1 || got.Providers[0].Name != "default" || !got.Providers[0].HasKey {
		t.Fatalf("providers = %+v, want the legacy endpoint as \"default\" with a key", got.Providers)
	}
	if got.Roles["vision"] != (roleView{"default", "v1"}) {
		t.Fatalf("vision role = %+v", got.Roles["vision"])
	}

	save := `{"providers":[
		{"name":"default","kind":"custom","base_url":"http://legacy/v1"},
		{"name":"router","kind":"openrouter","base_url":"https://openrouter.ai/api/v1/","api_key":"or-secret"}],
	 "roles":{"vision":{"provider":"router","model":"qwen-vl"},"embed":{"provider":"default","model":"e2"},
	          "segment":{"provider":"","model":""},"audio":{"provider":"","model":""}}}`
	if code, body := doAs(t, "PUT", srv.URL+"/api/settings/models", "", save); code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, body)
	}
	cfg, _, err := raglit.LoadConfig(home)
	must(t, err)
	if cfg.Providers["default"].APIKey != "legacy-secret" {
		t.Errorf("the legacy key was not carried into provider default")
	}
	if p := cfg.Providers["router"]; p.APIKey != "or-secret" || p.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("router = %+v", p)
	}
	if cfg.VisionProvider != "router" || cfg.VisionModel != "qwen-vl" || cfg.EmbedModel != "e2" {
		t.Errorf("roles not saved: %+v", cfg)
	}
	if cfg.OCR.CheapEngine != "tesseract" || cfg.Project != "p" || cfg.BaseURL != "http://legacy/v1" {
		t.Errorf("keys the page does not own were changed: ocr=%q project=%q base_url=%q", cfg.OCR.CheapEngine, cfg.Project, cfg.BaseURL)
	}
	if st, _ := os.Stat(home.ConfigPath()); st.Mode().Perm() != 0o600 {
		t.Errorf("config.json mode %v, want 0600", st.Mode().Perm())
	}

	// Vision None is allowed; a role on an unknown provider and a bad name are not.
	for name, body := range map[string]string{
		"unknown provider": `{"providers":[{"name":"a","base_url":"http://a/v1"}],"roles":{"embed":{"provider":"b","model":"m"}}}`,
		"bad name":         `{"providers":[{"name":"A B","base_url":"http://a/v1"}],"roles":{}}`,
		"bad url":          `{"providers":[{"name":"a","base_url":"ftp://a"}],"roles":{}}`,
	} {
		if code, _ := doAs(t, "PUT", srv.URL+"/api/settings/models", "", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", name, code)
		}
	}
}

// Listing a provider's models reports what each accepts, in corrallm's and
// OpenRouter's shapes, and reports an unreachable provider instead of failing.
func TestModelSettings_ListModels(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[
			{"id":"vl","architecture":{"input_modalities":["text","image"]}},
			{"id":"emb","type":"embed"},
			{"id":"txt"}]}`))
	}))
	defer up.Close()
	srv, _ := settingsServer(t, `{}`)

	code, body := doAs(t, "POST", srv.URL+"/api/settings/models/list", "", `{"base_url":"`+up.URL+`/v1","api_key":"k"}`)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var out struct {
		Models       []modelView `json:"models"`
		Capabilities bool        `json:"capabilities"`
	}
	must(t, json.Unmarshal([]byte(body), &out))
	by := map[string]modelView{}
	for _, m := range out.Models {
		by[m.ID] = m
	}
	if !out.Capabilities || !by["vl"].Vision || !by["emb"].Embed || by["txt"].Vision {
		t.Fatalf("models = %+v caps=%v", out.Models, out.Capabilities)
	}
	_, body = doAs(t, "POST", srv.URL+"/api/settings/models/list", "", `{"base_url":"`+up.URL+`/v1","api_key":"wrong"}`)
	if !strings.Contains(body, `"error"`) {
		t.Errorf("a refused listing should come back as an error field: %s", body)
	}
}

// With auth on, only the admin reads or changes model settings.
func TestModelSettings_AdminOnly(t *testing.T) {
	srv, fi := authTestServer(t)
	alice := fi.token(t, "alice", "raglit", fi.key)
	if code, _ := doAs(t, "GET", srv.URL+"/api/settings/models", alice, ""); code != http.StatusForbidden {
		t.Errorf("non-admin GET: %d, want 403", code)
	}
	if code, _ := doAs(t, "PUT", srv.URL+"/api/settings/models", alice, `{"providers":[],"roles":{}}`); code != http.StatusForbidden {
		t.Errorf("non-admin PUT: %d, want 403", code)
	}
}

// A role with no model shows no provider, not the legacy one.
func TestModelSettings_EmptyRoleHasNoProvider(t *testing.T) {
	out := modelSettingsFrom(raglit.Config{BaseURL: "http://x/v1", VisionModel: "v"})
	if out.Body.Roles["vision"].Provider != "default" || out.Body.Roles["audio"].Provider != "" || out.Body.Roles["segment"].Provider != "" {
		t.Fatalf("roles = %+v", out.Body.Roles)
	}
}
