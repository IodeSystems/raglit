package raglit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfigRoundTripAndInited(t *testing.T) {
	home := Home(filepath.Join(t.TempDir(), "h"))

	if Inited(home) {
		t.Fatal("fresh home should not be inited")
	}
	if _, ok, err := LoadConfig(home); ok || err != nil {
		t.Fatalf("missing config: want (_,false,nil), got ok=%v err=%v", ok, err)
	}

	want := Config{
		BaseURL:     "https://api.openai.com/v1",
		APIKey:      "sk-secret",
		VisionModel: "gpt-4o",
		EmbedModel:  "text-embedding-3-small",
	}
	if err := SaveConfig(home, want); err != nil {
		t.Fatal(err)
	}
	if !Inited(home) {
		t.Fatal("home should be inited after SaveConfig")
	}
	got, ok, err := LoadConfig(home)
	if err != nil || !ok {
		t.Fatalf("load after save: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// F11: the config names two DISTINCT ingest models — a transcription model for
// text zones and a multimodal describer for diagram/figure zones — so the
// single VisionModel no longer covers both roles.
func TestConfigPerKindModelsRoundTrip(t *testing.T) {
	home := Home(filepath.Join(t.TempDir(), "h"))

	cfg := Config{
		BaseURL:            "https://api.openai.com/v1",
		APIKey:             "sk-secret",
		VisionModel:        "gpt-4o",
		EmbedModel:         "text-embedding-3-small",
		TranscriptionModel: "chandra-ocr-2",
		DescribeModel:      "Qwen3-6-27B-MPT",
	}
	if err := SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadConfig(home)
	if err != nil || !ok {
		t.Fatalf("load after save: ok=%v err=%v", ok, err)
	}
	if got.TranscriptionModel != "chandra-ocr-2" || got.DescribeModel != "Qwen3-6-27B-MPT" {
		t.Fatalf("per-kind models lost in round-trip: %+v", got)
	}
	if !got.PerKindModelsConfigured() {
		t.Fatal("config with both per-kind models must report PerKindModelsConfigured")
	}

	// The live default: init writes no per-kind models, and a config without
	// them round-trips empty — the routing surface exists but is off.
	bare := Config{BaseURL: "u", APIKey: "k", VisionModel: "gpt-4o", EmbedModel: "e"}
	if err := SaveConfig(home, bare); err != nil {
		t.Fatal(err)
	}
	got2, _, _ := LoadConfig(home)
	if got2.TranscriptionModel != "" || got2.DescribeModel != "" {
		t.Fatalf("empty per-kind models must stay empty: %+v", got2)
	}
	if got2.PerKindModelsConfigured() {
		t.Fatal("no per-kind models configured — routing must be a no-op")
	}
}

// A provider's key is its literal key, else its environment variable, else its
// file's trimmed contents.
func TestProviderKey(t *testing.T) {
	f := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(f, []byte(" file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAGLIT_TEST_PROVIDER_KEY", "env-secret")
	for _, c := range []struct {
		p    Provider
		want string
	}{
		{Provider{APIKey: "literal", APIKeyEnv: "RAGLIT_TEST_PROVIDER_KEY"}, "literal"},
		{Provider{APIKeyEnv: "RAGLIT_TEST_PROVIDER_KEY", APIKeyFile: f}, "env-secret"},
		{Provider{APIKeyEnv: "RAGLIT_TEST_UNSET_KEY", APIKeyFile: f}, "file-secret"},
		{Provider{APIKeyFile: filepath.Join(t.TempDir(), "absent")}, ""},
	} {
		if got := c.p.Key(); got != c.want {
			t.Errorf("%+v: Key = %q, want %q", c.p, got, c.want)
		}
	}
}
