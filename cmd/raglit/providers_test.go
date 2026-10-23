package main

import (
	"testing"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

func resolvedFlags(t *testing.T, cfg raglit.Config, args ...string) *llmFlags {
	t.Helper()
	home := raglit.Home(t.TempDir())
	if err := raglit.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	lf.resolve(home)
	return lf
}

// Each role calls its own provider; segment with no model of its own follows
// vision; a role with no provider uses the legacy base_url.
func TestProviders_PerRoleEndpoints(t *testing.T) {
	t.Setenv("RAGLIT_LLM_KEY", "env-key")
	cfg := raglit.Config{
		BaseURL: "http://legacy/v1", APIKey: "legacy-key",
		VisionModel: "v", EmbedModel: "e",
		Providers: map[string]raglit.Provider{
			"router": {Kind: "openrouter", BaseURL: "http://router/v1", APIKey: "router-key"},
			"lite":   {Kind: "litellm", BaseURL: "http://lite/v1"},
		},
		VisionProvider: "router",
		EmbedProvider:  "lite",
	}
	lf := resolvedFlags(t, cfg)
	if lf.vision != (endpoint{"http://router/v1", "router-key"}) {
		t.Errorf("vision = %+v", lf.vision)
	}
	if lf.segment != lf.vision {
		t.Errorf("segment with no model of its own = %+v, want the vision endpoint", lf.segment)
	}
	// A named provider without a key sends none — not the legacy env key.
	if lf.embed != (endpoint{"http://lite/v1", ""}) {
		t.Errorf("embed = %+v", lf.embed)
	}

	cfg.VisionProvider = ""
	lf = resolvedFlags(t, cfg)
	if lf.vision != (endpoint{"http://legacy/v1", "env-key"}) {
		t.Errorf("no provider = %+v, want the legacy endpoint (env key first, as before)", lf.vision)
	}

	// --llm-url / --llm-key override every role.
	cfg.VisionProvider = "router"
	lf = resolvedFlags(t, cfg, "--llm-url", "http://flag/v1", "--llm-key", "flag-key")
	for name, ep := range map[string]endpoint{"vision": lf.vision, "embed": lf.embed} {
		if ep != (endpoint{"http://flag/v1", "flag-key"}) {
			t.Errorf("%s with flags = %+v", name, ep)
		}
	}
}
