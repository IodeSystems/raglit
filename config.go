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
	// EmbedLimitChars caps a fragment's size to what the embed model accepts as one
	// input — probed once and stored (DiscoverEmbedLimit), so the deterministic
	// fragmenter's ceiling is bounded by the model, not by taste. 0 = not probed
	// (the fragmenter falls back to FragWindow uncapped).
	EmbedLimitChars int `json:"embed_limit_chars,omitempty"`
	// SegmentInputLimitChars caps ONE segmentation REQUEST — prompt, carried-over
	// fragment and page text — to what the chat endpoint accepts, probed once and
	// stored (llm.DiscoverContext). The mirror of EmbedLimitChars on the other
	// side of the call: that one bounds what comes back, this one bounds what is
	// sent. 0 = not probed (no cap). Set it to override the probe on an endpoint
	// that accepts anything and then fails on it.
	SegmentInputLimitChars int `json:"segment_input_limit_chars,omitempty"`
	// FragWindow / FragStride / FragFloor tune the deterministic overlapping-window
	// text fragmenter (chars). 0 → defaults (9000 / 6000 / 3000). window > stride
	// gives the overlap; floor folds a short tail. They feed frag_recipe, so a
	// change marks the affected documents for reprocessing.
	FragWindow int `json:"frag_window,omitempty"`
	FragStride int `json:"frag_stride,omitempty"`
	FragFloor  int `json:"frag_floor,omitempty"`
	// DefaultIndex is the index used when a command gives no --index. Empty →
	// "default". Set it in the wizard to make one named index your working default.
	DefaultIndex string `json:"default_index,omitempty"`
	// Project names this project. On the SHARED daemon it namespaces every index
	// this client touches (daemon index = "<project>__<local>"), so two projects
	// both using index "default" don't collide, and a project's "search all" stays
	// within its own indexes. Required to start a daemon-routed client (serve/CLI);
	// --embedded/--db (single-session, in-process) don't need it. Set in the wizard.
	Project string `json:"project,omitempty"`
	// Shared lists OTHER project namespaces this project also SEARCHES (reads only).
	// A "search all" spans "<project>__*" plus each "<shared>__*", so common docs
	// (e.g. ~/doc indexed once under a project named "shared") are visible from
	// every project that opts in — without duplicating them per project. Writes
	// (ingest/branch) still target this project only. Empty → fully isolated.
	Shared []string `json:"shared,omitempty"`
	// Watch, when true, tells the daemon to keep this project's configured source
	// roots fresh: after you register it (a `raglit sync` with watch:true, or
	// `raglit watch`), the daemon re-scans the roots on an interval and re-ingests
	// changed files / drops deleted ones. Needs `indexes` (the roots to watch).
	Watch bool `json:"watch,omitempty"`

	// WritebackTranscriptionMd materialises <doc>.raglit-transcription.md beside
	// every ingested document, project-wide. A per-index setting of the same name
	// overrides this for that index.
	WritebackTranscriptionMd bool `json:"writeback_transcription_md,omitempty"`
	// ExtractEmailAttachments writes a mail archive's attachments into
	// <archive>.raglit-attachments/ beside it, project-wide, so the documents
	// inside an .eml/.mbox become ordinary files the next sync indexes. A
	// per-index setting of the same name overrides this for that index.
	ExtractEmailAttachments bool `json:"extract_email_attachments,omitempty"`
	// DaemonURL, when set, makes this a CLIENT config: commands route to the
	// raglit daemon at this URL (http(s)://host:port) instead of opening a local
	// index. The daemon owns storage (scoped per index, under its own home), so
	// the local .raglit/ then holds config only. Precedence for the effective
	// daemon: --daemon flag > $RAGLIT_DAEMON > this. Empty → local (embedded) mode.
	DaemonURL string `json:"daemon_url,omitempty"`
	// OCR configures the cheap first-pass tier of the OCR cascade. Zero value →
	// VLM-only (every page transcribed by the vision model).
	OCR OCRConfig `json:"ocr,omitempty"`
	// ImageEmbed optionally configures an image embedder for FIGURES: when its
	// Model is set, a figure is embedded from its IMAGE instead of its description.
	// nomic-embed-vision-v1.5 shares nomic-embed-text's space, so image figures stay
	// searchable by the same text query — requires EmbedModel to be nomic-embed-text.
	ImageEmbed ImageEmbedConfig `json:"image_embed,omitempty"`

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
	// WritebackTranscriptionMd materialises <doc>.raglit-transcription.md beside
	// each ingested document: the page-delineated text the pipeline already
	// produced. Off by default because it writes into the corpus, which is not
	// something an indexer should do uninvited.
	WritebackTranscriptionMd bool `json:"writeback_transcription_md,omitempty"`
	// ExtractEmailAttachments writes a mail archive's attachments into
	// <archive>.raglit-attachments/ beside it, with a MANIFEST.md recording which
	// message each came from. Off by default for the same reason: an archive can
	// carry 69 files and putting them in somebody's corpus uninvited is not an
	// indexer's call. Unlike a transcription the extracted files ARE indexable —
	// they are originals that travelled inside an envelope, not derived output —
	// so the next `sync` picks them up as ordinary files.
	ExtractEmailAttachments bool `json:"extract_email_attachments,omitempty"`
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

// ImageEmbedConfig configures the optional figure IMAGE embedder (see
// Config.ImageEmbed). Model empty → disabled (figures embed their description).
type ImageEmbedConfig struct {
	Model  string `json:"model,omitempty"`   // e.g. nomic-embed-vision-v1.5; empty → disabled
	URL    string `json:"url,omitempty"`     // full endpoint; empty → Nomic Atlas image API
	APIKey string `json:"api_key,omitempty"` // empty → reuse the main api_key
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
	// DescribeFigures escalates born-digital PDF pages that carry an embedded image
	// to the VLM so their figures are described inline (§3a figure gate). OFF by
	// default: it forces those pages onto the vision/llm-seg path (costly).
	DescribeFigures bool `json:"describe_figures,omitempty"`
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
