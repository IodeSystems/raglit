package main

import (
	"testing"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// llmFlags.models is where raglit fills the library's Models, so a role
// taking another role's endpoint here would pass every NewWorker test. Each
// role gets its own named provider, so a crossed wire shows as a wrong URL.
func TestLLMFlags_ModelsMapsEachRole(t *testing.T) {
	t.Setenv("RAGLIT_LLM_KEY", "")
	cfg := raglit.Config{
		BaseURL: "http://legacy/v1", APIKey: "legacy-key",
		VisionModel: "vis", SegmentModel: "seg", EmbedModel: "emb",
		IdentityModel: "ident", AudioModel: "stt",
		Providers: map[string]raglit.Provider{
			"pv": {Kind: "litellm", BaseURL: "http://vision/v1", APIKey: "vision-key"},
			"ps": {Kind: "litellm", BaseURL: "http://segment/v1", APIKey: "segment-key"},
			"pe": {Kind: "litellm", BaseURL: "http://embed/v1", APIKey: "embed-key"},
			"pi": {Kind: "litellm", BaseURL: "http://identity/v1", APIKey: "identity-key"},
			"pa": {Kind: "litellm", BaseURL: "http://audio/v1", APIKey: "audio-key"},
		},
		VisionProvider:          "pv",
		SegmentProvider:         "ps",
		EmbedProvider:           "pe",
		IdentityProvider:        "pi",
		AudioProvider:           "pa",
		FragWindow:              1200,
		FragStride:              900,
		FragFloor:               100,
		EmbedLimitChars:         5000,
		EmbedLimitTokens:        2000,
		SegmentInputLimitTokens: 3000,
		ModelChannelMax:         3,
	}
	home := raglit.Home(t.TempDir())
	if err := raglit.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	lf := resolvedFlagsAt(t, home)
	m := lf.models(home)

	for _, c := range []struct {
		name string
		got  raglit.ModelRole
		want raglit.ModelRole
	}{
		{"vision", m.Vision, raglit.ModelRole{BaseURL: "http://vision/v1", APIKey: "vision-key", Model: "vis"}},
		{"segment", m.Segment, raglit.ModelRole{BaseURL: "http://segment/v1", APIKey: "segment-key", Model: "seg"}},
		{"embed", m.Embed, raglit.ModelRole{BaseURL: "http://embed/v1", APIKey: "embed-key", Model: "emb"}},
		{"identity", m.Identity, raglit.ModelRole{BaseURL: "http://identity/v1", APIKey: "identity-key", Model: "ident"}},
		{"audio", m.Audio, raglit.ModelRole{BaseURL: "http://audio/v1", APIKey: "audio-key", Model: "stt"}},
	} {
		if c.got != c.want {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}
	if m.FragWindow != 1200 || m.FragStride != 900 || m.FragFloor != 100 {
		t.Errorf("frag = %d/%d/%d, want 1200/900/100", m.FragWindow, m.FragStride, m.FragFloor)
	}
	if m.EmbedLimitChars != 5000 || m.EmbedLimitTokens != 2000 || m.SegmentInputLimitTokens != 3000 {
		t.Errorf("limits = %d/%d/%d, want 5000/2000/3000",
			m.EmbedLimitChars, m.EmbedLimitTokens, m.SegmentInputLimitTokens)
	}
	if m.Channels == nil || m.Channels != lf.chans || m.Channels.Max != 3 {
		t.Errorf("channels = %p (max %v), want the flags' shared registry with max 3", m.Channels, m.Channels)
	}

	// AudioBaseURL overrides the audio provider's URL, keeping its key.
	cfg.AudioBaseURL = "http://oidio/v1"
	// An identity model with no provider is on the legacy base_url, not the
	// segment endpoint: endpointFor("") is the legacy endpoint.
	cfg.IdentityProvider = ""
	if err := raglit.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	m = resolvedFlagsAt(t, home).models(home)
	if want := (raglit.ModelRole{BaseURL: "http://oidio/v1", APIKey: "audio-key", Model: "stt"}); m.Audio != want {
		t.Errorf("audio with audio_base_url = %+v, want %+v", m.Audio, want)
	}
	if want := (raglit.ModelRole{BaseURL: "http://legacy/v1", APIKey: "legacy-key", Model: "ident"}); m.Identity != want {
		t.Errorf("identity model with no provider = %+v, want %+v", m.Identity, want)
	}

	// No identity model → the segment model on the segment endpoint.
	cfg.IdentityModel = ""
	if err := raglit.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	m = resolvedFlagsAt(t, home).models(home)
	if want := (raglit.ModelRole{BaseURL: "http://segment/v1", APIKey: "segment-key", Model: "seg"}); m.Identity != want {
		t.Errorf("identity with no model of its own = %+v, want %+v", m.Identity, want)
	}

	// no_identity turns the role off whatever models are set.
	cfg.NoIdentity = true
	if err := raglit.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	m = resolvedFlagsAt(t, home).models(home)
	if m.Identity != (raglit.ModelRole{}) {
		t.Errorf("identity with no_identity = %+v, want empty", m.Identity)
	}
}

// resolvedFlagsAt is resolvedFlags on a home whose config is already written.
func resolvedFlagsAt(t *testing.T, home raglit.Home) *llmFlags {
	t.Helper()
	lf := addLLMFlags(flag.NewFlagSet("t", flag.ContinueOnError))
	lf.resolve(home)
	return lf
}
