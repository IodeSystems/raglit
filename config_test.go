package raglit

import (
	"path/filepath"
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
	if got != want {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}
