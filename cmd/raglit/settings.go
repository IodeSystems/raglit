package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/iodesystems/raglit"
)

// Model settings over the API: providers, then a provider and model per role
// (USER 2026-10-22, plan/model-settings.md). Admin-only with auth on, because
// it changes what every index is read and embedded with.
//
// A save patches config.json in place, touching only the keys it owns, so
// settings this page does not model survive. The daemon then reloads itself
// (SIGHUP → reloadOnHUP), which waits for the running job to finish. Keys are
// write-only: they are never sent back, only whether one is set.

// legacyProvider names the pre-provider base_url/api_key when shown as a
// provider. Saving writes it as a real provider.
const legacyProvider = "default"

// modelRoles in display order. Segment and audio may be empty.
var modelRoles = []string{"vision", "embed", "segment", "audio"}

type providerView struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	HasKey  bool   `json:"has_key"`
}

type roleView struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type presetView struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	BaseURL string `json:"base_url"`
	// KeyHint says whether the provider needs a token: "required" or "optional".
	KeyHint string `json:"key_hint"`
}

var providerPresets = []presetView{
	{Kind: "custom", Label: "Custom (OpenAI-compatible)", BaseURL: "", KeyHint: "optional"},
	{Kind: "openrouter", Label: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", KeyHint: "required"},
	{Kind: "litellm", Label: "LiteLLM", BaseURL: "http://localhost:4000/v1", KeyHint: "optional"},
}

type modelSettingsOut struct {
	Body struct {
		Providers []providerView      `json:"providers"`
		Roles     map[string]roleView `json:"roles"`
		Presets   []presetView        `json:"presets"`
		Tesseract bool                `json:"tesseract" doc:"tesseract is on the daemon's PATH, so scans are readable with no vision model"`
	}
}

func modelSettingsFrom(cfg raglit.Config) *modelSettingsOut {
	out := &modelSettingsOut{}
	b := &out.Body
	b.Presets = providerPresets
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := cfg.Providers[n]
		b.Providers = append(b.Providers, providerView{Name: n, Kind: firstNonEmpty(p.Kind, "custom"), BaseURL: p.BaseURL, HasKey: p.APIKey != ""})
	}
	// The legacy endpoint, shown as a provider while any role still uses it.
	legacy := func(p string) string {
		if p == "" && cfg.BaseURL != "" {
			return legacyProvider
		}
		return p
	}
	usesLegacy := false
	b.Roles = map[string]roleView{
		"vision":  {Provider: legacy(cfg.VisionProvider), Model: cfg.VisionModel},
		"embed":   {Provider: legacy(cfg.EmbedProvider), Model: cfg.EmbedModel},
		"segment": {Provider: legacy(cfg.SegmentProvider), Model: cfg.SegmentModel},
		"audio":   {Provider: legacy(cfg.AudioProvider), Model: cfg.AudioModel},
	}
	for _, r := range b.Roles {
		if r.Provider == legacyProvider {
			usesLegacy = true
		}
	}
	if _, taken := cfg.Providers[legacyProvider]; usesLegacy && !taken {
		b.Providers = append([]providerView{{Name: legacyProvider, Kind: "custom", BaseURL: cfg.BaseURL, HasKey: cfg.APIKey != ""}}, b.Providers...)
	}
	_, err := exec.LookPath("tesseract")
	b.Tesseract = err == nil
	return out
}

func getModelSettingsOp(reg *raglit.Registry, home raglit.Home) func(context.Context, *struct{}) (*modelSettingsOut, error) {
	return func(ctx context.Context, _ *struct{}) (*modelSettingsOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		cfg, _, err := raglit.LoadConfig(home)
		if err != nil {
			return nil, err
		}
		return modelSettingsFrom(cfg), nil
	}
}

type providerIn struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	BaseURL string `json:"base_url"`
	// APIKey replaces the stored key when non-empty. Empty keeps the stored key
	// unless ClearKey is set.
	APIKey   string `json:"api_key,omitempty"`
	ClearKey bool   `json:"clear_key,omitempty"`
}

type saveModelSettingsIn struct {
	Body struct {
		Providers []providerIn        `json:"providers"`
		Roles     map[string]roleView `json:"roles"`
	}
}

type saveModelSettingsOut struct {
	Body struct {
		Saved bool `json:"saved"`
		// Reload is "queued": the daemon re-reads its config when no job is
		// running.
		Reload string `json:"reload"`
	}
}

var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// applyModelSettings validates in and writes it onto cfg's provider and role
// fields. Stored keys survive unless replaced or cleared.
func applyModelSettings(cfg *raglit.Config, in *saveModelSettingsIn) error {
	old := cfg.Providers
	provs := map[string]raglit.Provider{}
	for _, p := range in.Body.Providers {
		if !providerName.MatchString(p.Name) {
			return fmt.Errorf("provider name %q: use lowercase letters, digits and dashes", p.Name)
		}
		if _, dup := provs[p.Name]; dup {
			return fmt.Errorf("provider %q is listed twice", p.Name)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("provider %q: base URL must be http(s)://host…", p.Name)
		}
		key := p.APIKey
		if key == "" && !p.ClearKey {
			if o, ok := old[p.Name]; ok {
				key = o.APIKey
			} else if p.Name == legacyProvider {
				key = cfg.APIKey
			}
		}
		provs[p.Name] = raglit.Provider{Kind: p.Kind, BaseURL: strings.TrimRight(p.BaseURL, "/"), APIKey: key}
	}
	role := func(name string) (string, string, error) {
		r := in.Body.Roles[name]
		r.Model = strings.TrimSpace(r.Model)
		if r.Model == "" {
			return "", "", nil // role off (vision/audio None, segment → vision)
		}
		if _, ok := provs[r.Provider]; !ok {
			return "", "", fmt.Errorf("%s model %q needs a provider from the list", name, r.Model)
		}
		return r.Provider, r.Model, nil
	}
	var err error
	if cfg.VisionProvider, cfg.VisionModel, err = role("vision"); err != nil {
		return err
	}
	if cfg.EmbedProvider, cfg.EmbedModel, err = role("embed"); err != nil {
		return err
	}
	if cfg.SegmentProvider, cfg.SegmentModel, err = role("segment"); err != nil {
		return err
	}
	if cfg.AudioProvider, cfg.AudioModel, err = role("audio"); err != nil {
		return err
	}
	cfg.Providers = provs
	return nil
}

// settingsKeys are the config.json keys a save owns.
var settingsKeys = []string{
	"providers",
	"vision_provider", "vision_model",
	"embed_provider", "embed_model",
	"segment_provider", "segment_model",
	"audio_provider", "audio_model",
}

// patchConfig writes only settingsKeys from cfg into home's config.json,
// keeping every other key as it is on disk. Written to a temp file and renamed,
// so a crash cannot leave half a config.
func patchConfig(home raglit.Home, cfg raglit.Config) error {
	raw := map[string]json.RawMessage{}
	if b, err := os.ReadFile(home.ConfigPath()); err == nil {
		if err := json.Unmarshal(b, &raw); err != nil {
			return fmt.Errorf("config.json does not parse: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	full, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	fresh := map[string]json.RawMessage{}
	if err := json.Unmarshal(full, &fresh); err != nil {
		return err
	}
	for _, k := range settingsKeys {
		if v, ok := fresh[k]; ok {
			raw[k] = v
		} else {
			delete(raw, k) // omitempty: the value is empty now
		}
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := home.Ensure(); err != nil {
		return err
	}
	tmp := home.ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, home.ConfigPath())
}

// requestReload asks this daemon to reload (reloadOnHUP). Delayed so the
// response that triggered it is written first.
var requestReload = func() {
	time.AfterFunc(200*time.Millisecond, func() { _ = syscall.Kill(os.Getpid(), syscall.SIGHUP) })
}

func saveModelSettingsOp(reg *raglit.Registry, home raglit.Home) func(context.Context, *saveModelSettingsIn) (*saveModelSettingsOut, error) {
	return func(ctx context.Context, in *saveModelSettingsIn) (*saveModelSettingsOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		cfg, _, err := raglit.LoadConfig(home)
		if err != nil {
			return nil, err
		}
		if err := applyModelSettings(&cfg, in); err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		if err := patchConfig(home, cfg); err != nil {
			return nil, err
		}
		requestReload()
		out := &saveModelSettingsOut{}
		out.Body.Saved, out.Body.Reload = true, "queued"
		return out, nil
	}
}

type listModelsIn struct {
	Body struct {
		// Provider names a saved provider; its stored key is used.
		Provider string `json:"provider,omitempty"`
		// BaseURL/APIKey list a provider that is not saved yet (the add form).
		BaseURL string `json:"base_url,omitempty"`
		APIKey  string `json:"api_key,omitempty"`
	}
}

type modelView struct {
	ID     string `json:"id"`
	Vision bool   `json:"vision"`
	Embed  bool   `json:"embed"`
	Audio  bool   `json:"audio"`
	State  string `json:"state,omitempty"`
}

type listModelsOut struct {
	Body struct {
		Models []modelView `json:"models"`
		// Capabilities is whether the provider reports what its models accept.
		// False → the UI cannot filter by role and shows every model.
		Capabilities bool   `json:"capabilities"`
		Error        string `json:"error,omitempty"`
	}
}

func listModelsOp(reg *raglit.Registry, home raglit.Home) func(context.Context, *listModelsIn) (*listModelsOut, error) {
	return func(ctx context.Context, in *listModelsIn) (*listModelsOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		base, key := in.Body.BaseURL, in.Body.APIKey
		if in.Body.Provider != "" {
			cfg, _, err := raglit.LoadConfig(home)
			if err != nil {
				return nil, err
			}
			b, k, ok := cfg.Endpoint(in.Body.Provider)
			if !ok && in.Body.Provider != legacyProvider {
				return nil, huma.Error404NotFound("no provider named " + in.Body.Provider)
			}
			base = firstNonEmpty(base, b)
			key = firstNonEmpty(key, k)
		}
		out := &listModelsOut{}
		if base == "" {
			out.Body.Error = "no base URL"
			return out, nil
		}
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		ms, err := fetchModels(lctx, base, key)
		if err != nil {
			// Reported, not failed: the form shows it beside the URL.
			out.Body.Error = err.Error()
			return out, nil
		}
		out.Body.Capabilities = hasCapabilities(ms)
		for _, m := range ms {
			out.Body.Models = append(out.Body.Models, modelView{
				ID: m.ID, Vision: m.Vision, Embed: roleMatches(m, "embed"),
				Audio: m.Audio || m.Type == "stt" || strings.HasPrefix(m.Capability, "audio.stt"),
				State: m.State,
			})
		}
		return out, nil
	}
}

type reembedImpactIn struct {
	EmbedModel string `query:"embed_model" doc:"the embed model a save would set"`
}

type reembedImpactOut struct {
	Body struct {
		Indexes   int `json:"indexes"`
		Fragments int `json:"fragments"`
	}
}

// reembedImpactOp counts what changing the embed model would re-embed: every
// index whose vectors are recorded as another model's.
func reembedImpactOp(reg *raglit.Registry) func(context.Context, *reembedImpactIn) (*reembedImpactOut, error) {
	return func(ctx context.Context, in *reembedImpactIn) (*reembedImpactOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		out := &reembedImpactOut{}
		if in.EmbedModel == "" {
			return out, nil
		}
		for _, name := range reg.Names() {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			if cur := st.EmbedModel(); cur == "" || cur == in.EmbedModel {
				continue
			}
			n, err := st.FragmentCount()
			if err != nil {
				continue
			}
			out.Body.Indexes++
			out.Body.Fragments += n
		}
		return out, nil
	}
}
