package raglit

import (
	"encoding/json"
	"os"
)

// Config is raglit's model-connection setup, written by `raglit init` into
// <home>/config.json. It's OpenAI-standard: a base URL + token, plus the two
// model ids raglit needs — a vision model (image in → text, for OCR) and an
// embedding model (text in → vector, for --embed / vector search). Kept out of
// the index so the same corpus can be re-pointed at a different endpoint.
type Config struct {
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	VisionModel string `json:"vision_model"`
	EmbedModel  string `json:"embed_model"`
	// ContextTokens caches the model's discovered context window (see window.go)
	// so text/code ingestion doesn't re-probe it every run. 0 = not yet probed.
	ContextTokens int `json:"context_tokens,omitempty"`
	// DefaultIndex is the index used when a command gives no --index. Empty →
	// "default". Set it in the wizard to make one named index your working default.
	DefaultIndex string `json:"default_index,omitempty"`
}

// LoadConfig reads the home's config. exists is false (with nil error) when the
// home has not been initialized yet — the caller decides whether that's fatal.
func LoadConfig(h Home) (cfg Config, exists bool, err error) {
	b, err := os.ReadFile(h.ConfigPath())
	if os.IsNotExist(err) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

// SaveConfig writes cfg to <home>/config.json (0600 — it holds a token),
// creating the home layout if needed.
func SaveConfig(h Home, cfg Config) error {
	if err := h.Ensure(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.ConfigPath(), b, 0o600)
}

// Inited reports whether a home has a usable config.
func Inited(h Home) bool {
	_, ok, _ := LoadConfig(h)
	return ok
}
