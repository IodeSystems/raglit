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
	// Project names this project. On the SHARED daemon it namespaces every index
	// this client touches (daemon index = "<project>__<local>"), so two projects
	// both using index "default" don't collide, and a project's "search all" stays
	// within its own indexes. Required to start a daemon-routed client (serve/CLI);
	// --embedded/--db (single-session, in-process) don't need it. Set in the wizard.
	Project string `json:"project,omitempty"`
	// DaemonURL, when set, makes this a CLIENT config: commands route to the
	// raglit daemon at this URL (http(s)://host:port) instead of opening a local
	// index. The daemon owns storage (scoped per index, under its own home), so
	// the local .raglit/ then holds config only. Precedence for the effective
	// daemon: --daemon flag > $RAGLIT_DAEMON > this. Empty → local (embedded) mode.
	DaemonURL string `json:"daemon_url,omitempty"`
	// OCR configures the cheap first-pass tier of the OCR cascade. Zero value →
	// VLM-only (every page transcribed by the vision model).
	OCR OCRConfig `json:"ocr,omitempty"`

	// Ignore is this config's default exclude globs (project-scoped — it does not
	// affect other projects' configs). Merged with a built-in default (dot-dirs,
	// node_modules, vendor) and the per-index / per-root ignores; ignore wins.
	Ignore []string `json:"ignore,omitempty"`
	// Gitignore, when nil or true, also honors each root's .gitignore chain.
	Gitignore *bool `json:"gitignore,omitempty"`
	// Indexes declares named indexes and the source roots + rules that feed them,
	// for `raglit sync`. Multi-index: one project can define several.
	Indexes map[string]IndexConfig `json:"indexes,omitempty"`
}

// IndexConfig is one index's source definition: the roots to scan and the
// include/ignore globs that apply to them (overridable per root).
type IndexConfig struct {
	Roots   []Root   `json:"roots,omitempty"`
	Include []string `json:"include,omitempty"` // a file must match one to be indexed
	Ignore  []string `json:"ignore,omitempty"`  // merged with project + built-in ignore
}

// Root is a source directory, optionally with its own include/ignore overriding
// the index's. In JSON it is EITHER a bare path string OR {path, include, ignore}.
type Root struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Ignore  []string `json:"ignore,omitempty"`
}

// UnmarshalJSON accepts a bare string ("./src") or an object
// ({"path":"./gen","include":["*.go"]}).
func (r *Root) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.Path)
	}
	type raw Root
	return json.Unmarshal(b, (*raw)(r))
}

// OCRConfig selects and tunes the cheap first-pass OCR engine. The cascade tries
// this engine before the vision model and escalates only when the page looks
// like gibberish (see ocr.go, gibberish.go). CheapEngine="none" (the default)
// disables the cheap tier — the cascade is then VLM-only.
type OCRConfig struct {
	CheapEngine   string          `json:"cheap_engine,omitempty"`   // "none" | "tesseract" | "paddleocr"
	PaddleURL     string          `json:"paddle_url,omitempty"`     // sidecar base URL when cheap_engine=paddleocr
	TesseractBin  string          `json:"tesseract_bin,omitempty"`  // tesseract binary; "" → "tesseract"
	TesseractLang string          `json:"tesseract_lang,omitempty"` // -l language; "" → "eng"
	Gibberish     GibberishConfig `json:"gibberish,omitempty"`      // gate overrides; zero → precision-biased defaults
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
