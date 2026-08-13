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
	// EmbedLimitTokens is the same limit in the unit the embedder counts in, and
	// it is the one that decides. EmbedLimitChars is a conversion of it at two
	// characters per token; a scanned court brief is 1.57, so a fragment can sit
	// inside the character ceiling and still be refused. 0 = not established.
	EmbedLimitTokens int `json:"embed_limit_tokens,omitempty"`
	// SegmentInputLimitTokens caps ONE segmentation REQUEST — prompt, carried-over
	// fragment and page text — to what the chat endpoint accepts, asked once and
	// stored (the server's own n_ctx where it states one, else llm.DiscoverContext).
	// The mirror of EmbedLimitChars on the other side of the call: that one bounds
	// what comes back, this one bounds what is sent.
	//
	// TOKENS, because that is the unit every endpoint states its limit in.
	// EmbedLimitChars stays in characters because it sizes a fragment, and the
	// conversion between the two is not a constant: measured with the model's own
	// tokenizer, this corpus runs 4.66 chars/token on prose and 1.16 on a survey
	// legal description.
	//
	// 0 = not established (no cap). Set it to override on a proxy that accepts a
	// prompt its backend will later refuse — which cannot be measured from
	// outside.
	SegmentInputLimitTokens int `json:"segment_input_limit_tokens,omitempty"`
	// FragWindow / FragStride / FragFloor tune the deterministic overlapping-window
	// text fragmenter (chars). 0 → defaults (9000 / 6000 / 3000). window > stride
	// gives the overlap; floor folds a short tail. They feed frag_recipe, so a
	// change marks the affected documents for reprocessing.
	FragWindow int `json:"frag_window,omitempty"`
	FragStride int `json:"frag_stride,omitempty"`
	FragFloor  int `json:"frag_floor,omitempty"`
	// IdentityModel names the chat model that says what a document IS — a
	// caption, a summary and a kind, asked once per document on the assembled
	// transcript (identity.go). Empty → the vision model, which is already
	// configured and is a chat model; set this to caption with a stronger or
	// cheaper text-only model than the one doing OCR.
	IdentityModel string `json:"identity_model,omitempty"`
	// IdentitySlots is how many captioning requests are in the model at once.
	// 0 → DefaultIdentitySlots (2), which is what this endpoint serves
	// concurrently. Raising it past what the server actually runs does not make
	// anything faster: the extra requests wait INSIDE the server, where raglit
	// cannot see them, cannot resume them, and cannot tell them apart from an
	// ingest job's OCR call waiting for the same slot.
	IdentitySlots int `json:"identity_slots,omitempty"`

	// ModelChannelMax caps how wide any one model's admission channel may grow
	// (modelchan.go). 0 → the built-in ceiling.
	//
	// Every other number in that controller is LEARNED — a model starts at one
	// slot and widens only while calls succeed, halving whenever the server
	// pushes back. This is the exception, because the right ceiling depends on
	// what kind of endpoint is behind the name and no amount of evidence
	// distinguishes "has not said no yet" from "will take a hundred": a card
	// serving one model locally is never going past a handful, while a hosted
	// provider bought for throughput will, and there the ceiling would be the
	// only thing limiting it.
	ModelChannelMax int `json:"model_channel_max,omitempty"`
	// NoIdentity turns document identity off. Documents then carry only the
	// filename they arrived with — which for a scanner-named corpus is a list
	// nobody can navigate, so this is for a corpus whose names are already good
	// (a code tree) or an endpoint you do not want the extra call on.
	NoIdentity bool `json:"no_identity,omitempty"`
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
	// SegmentModel splits transcribed TEXT into fragments. Empty → the vision
	// model, which is what this always used to be.
	//
	// They are different jobs and the best model for one is not the best for the
	// other. The segmenter is asked for a structured tool call over text; an OCR
	// specialist is chosen for reading pixels and need not do the first well.
	// Measured on a 2-page 1947 deed, same cached OCR, four arms:
	//
	//	chandra + markup     degraded — no valid JSON
	//	Qwen    + markup     done, dropped 1184 of 1234 chars
	//	chandra + flattened  degraded — no valid JSON
	//	Qwen    + flattened  done, clean
	//
	// Corpus-wide the same split: chandra degraded 29 of 116 documents (25%),
	// Qwen 4 of 254 (1.6%).
	SegmentModel string `json:"segment_model,omitempty"`
	// DaemonURL, when set, makes this a CLIENT config: commands route to the
	// raglit daemon at this URL (http(s)://host:port) instead of opening a local
	// index. The daemon owns storage (scoped per index, under its own home), so
	// the local .raglit/ then holds config only. Precedence for the effective
	// daemon: --daemon flag > $RAGLIT_DAEMON > this. Empty → local (embedded) mode.
	DaemonURL string `json:"daemon_url,omitempty"`
	// OCR configures the cheap tier — either as the cascade's first pass or as
	// the vision model's spelling reference (OCRConfig.Mode). Zero value →
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
	// WritebackTranscriptionMd NO LONGER DOES ANYTHING, and is kept only so an
	// existing config still parses.
	//
	// It made ingest write <doc>.raglit-transcription.md beside every document it
	// read — 407 files into one legal evidence tree, each duplicating text raglit
	// already held in `pages`, in the fragments, and on /api/doc-detail. raglit
	// then needed IsGeneratedSidecar, a builtinIgnore entry and a refusal inside
	// the writer itself to avoid tripping over its own output.
	//
	// `raglit transcribe` still writes one. The difference is that a person asked.
	WritebackTranscriptionMd bool `json:"writeback_transcription_md,omitempty"`
	// ExtractEmailAttachments writes a mail archive's attachments into
	// <archive>.raglit-attachments/ beside it, with a MANIFEST.md recording which
	// message each came from. Off by default for the same reason: an archive can
	// carry 69 files and putting them in somebody's corpus uninvited is not an
	// indexer's call. Unlike a transcription the extracted files ARE indexable —
	// they are originals that travelled inside an envelope, not derived output —
	// so the next `sync` picks them up as ordinary files.
	ExtractEmailAttachments bool `json:"extract_email_attachments,omitempty"`
	// OCRStrategy names an entry in OCRConfig.Strategies. Empty → the project's
	// OCR.Strategy, else today's behavior.
	//
	// Per index because that is the grain at which document KIND is already
	// declared here: `records/` is recorded surveys and `correspondence/` is
	// letters, and they want different amounts of work per page. Attaching the
	// policy to the corpus rather than to the command is the whole point — a
	// strategy retyped as a flag is one that gets forgotten.
	OCRStrategy string `json:"ocr_strategy,omitempty"`
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
	// Mode decides what the cheap engine is for.
	//
	//   "cascade" (default) — it reads first, and the vision model is paid for
	//     only when the gibberish gate rejects the result. A cost decision.
	//   "assist" — the vision model reads every page, with the cheap engine's
	//     WORDS as a spelling reference and its numbers removed. See OCR.Assist:
	//     the two readers fail differently, and this is how each one's strength
	//     is used without importing the other's errors.
	//
	// A corpus where a misread digit is expensive wants "assist"; a corpus where
	// throughput matters more than a certificate number wants "cascade".
	Mode string `json:"mode,omitempty"`

	// Strategies are named bundles of read policy, selectable per index via
	// IndexConfig.OCRStrategy. They exist because one corpus is not one kind of
	// page: a records/ folder of recorded surveys wants descent, tiling and a
	// hint about monument calls, while correspondence/ wants none of it and
	// should not pay for the check. Before this the knobs lived only on
	// `raglit regions` flags, so a policy could not be attached to a corpus at
	// all — it had to be retyped per invocation and was forgotten by the next.
	Strategies map[string]StrategyConfig `json:"strategies,omitempty"`

	// Strategy names the default for indexes that do not choose one. Empty means
	// the zero StrategyConfig, which is today's behavior exactly.
	Strategy string `json:"strategy,omitempty"`
}

// StrategyConfig is how much work a page is worth.
//
// Every field is zero-valued to today's behavior, so adding this section changes
// nothing until something is set. That matters more than brevity here: this
// governs model spend, and a config format whose defaults are not the current
// behavior turns an upgrade into a bill.
type StrategyConfig struct {
	// Descend is RegionReader.MaxDepth. 0 reads the sheet and stops, which is
	// what every page does today.
	Descend int `json:"descend,omitempty"`
	// Tile subdivides a large low-resolution DRAWING geometrically instead of
	// asking the model where to look — arithmetic instead of a call, and on an
	// E-size sheet the asking is the part that does not work.
	Tile bool `json:"tile,omitempty"`
	// Hint is threaded into every prompt. Cheapest lever here by a distance: the
	// model proposing regions is looking at a view where the thing you want may
	// be physically unresolvable, so it cannot propose what it cannot see. A
	// sentence naming the target beats any amount of extra budget.
	Hint string `json:"hint,omitempty"`

	// Budgets. Zero → RegionReader's own defaults (8 / 2 / 40 / 4).
	MaxCalls       int     `json:"max_calls,omitempty"`
	MaxChildren    int     `json:"max_children,omitempty"`
	MaxTransforms  int     `json:"max_transforms,omitempty"`
	MaxEscalations int     `json:"max_escalations,omitempty"`
	MinRegionIn    float64 `json:"min_region_in,omitempty"`

	// Render overrides the automatic per-page resolution policy.
	Render RenderPolicy `json:"render,omitempty"`

	// AutoDescend descends WITHOUT being asked, but only for a page the pixels
	// say needs it: the same low-resolution test the descent already applies
	// internally (below half the letter-page token density). Measured over a 998
	// page corpus that is 14 pages in 11 documents — 1.4% — so it is affordable
	// in a way "descend everything" is not, and it targets exactly the sheets a
	// single-shot read fails on. Off by default; Descend still bounds it.
	AutoDescend bool `json:"auto_descend,omitempty"`
}

// RenderPolicy is the automatic per-page resolution rule, which was four package
// constants and therefore the one thing here that genuinely required a rebuild
// to change.
//
// The rule: measure the page's own glyph height with the cheap engine, and if it
// is below SmallTextGlyphPx re-render at whatever DPI brings it to
// TargetGlyphPx, capped at MaxDPI. Zero fields keep the measured defaults —
// 200/600/20/14 — which were chosen on this corpus and should not be moved
// casually. Raising TargetGlyphPx costs tokens on every small-text page.
type RenderPolicy struct {
	BaseDPI          int `json:"base_dpi,omitempty"`            // 0 → 200
	MaxDPI           int `json:"max_dpi,omitempty"`             // 0 → 600
	TargetGlyphPx    int `json:"target_glyph_px,omitempty"`     // 0 → 20
	SmallTextGlyphPx int `json:"small_text_glyph_px,omitempty"` // 0 → 14
}

// resolved fills a RenderPolicy's zero fields with the package defaults, so
// callers never branch on "was this set".
func (r RenderPolicy) resolved() RenderPolicy {
	if r.BaseDPI <= 0 {
		r.BaseDPI = baseRenderDPI
	}
	if r.MaxDPI <= 0 {
		r.MaxDPI = maxRenderDPI
	}
	if r.TargetGlyphPx <= 0 {
		r.TargetGlyphPx = targetGlyphPx
	}
	if r.SmallTextGlyphPx <= 0 {
		r.SmallTextGlyphPx = smallTextGlyphPx
	}
	return r
}

// StrategyFor resolves the read policy for one index: the index's own choice,
// else the project default, else the zero value (today's behavior).
//
// An index naming a strategy that does not exist gets the zero value rather than
// an error, and that is deliberate — a typo'd strategy name must not stop an
// ingest, and the alternative (silently using the default strategy) would hide
// the typo behind plausible output. Callers that want to complain can check
// StrategyNamed.
func (c Config) StrategyFor(index string) StrategyConfig {
	if ic, ok := c.Indexes[index]; ok && ic.OCRStrategy != "" {
		s, _ := c.StrategyNamed(ic.OCRStrategy)
		return s
	}
	s, _ := c.StrategyNamed(c.OCR.Strategy)
	return s
}

// StrategyNamed looks a strategy up by name. ok=false for an empty or unknown
// name, in which case the zero StrategyConfig is returned.
func (c Config) StrategyNamed(name string) (StrategyConfig, bool) {
	if name == "" {
		return StrategyConfig{}, false
	}
	s, ok := c.OCR.Strategies[name]
	return s, ok
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
