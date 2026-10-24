package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// What raglit can do here, and what more would add: `doctor --json`, and the
// "raglit/setup" notification `serve` sends after it starts.
//
// USER 2026-10-24 (via dun): "can raglit be as pleasant to install as
// poly-lsp? … optional embedding/image/summary provider/model, with a note of
// its capability and what would be needed to make it more useful." The shape
// follows poly-lsp-mcp's doctor (camelCase keys, install {cmd, needs}, update
// verbatim) so dun reads one shape for every tool. Field names are a contract
// with dun; renaming one breaks it.

// doctorReport is `raglit doctor --json`.
type doctorReport struct {
	Binary     string `json:"binary"`
	Build      string `json:"build"`
	Platform   string `json:"platform"`
	Home       string `json:"home"`
	Configured bool   `json:"configured"` // a config.json exists
	// Daemon is the shared daemon's URL when it answered, "" when not.
	Daemon string `json:"daemon"`
	setupSummary
	Update *updateInfo `json:"update,omitempty"` // absent when nothing is known
}

// setupSummary is the part `serve` also sends as the "raglit/setup"
// notification.
type setupSummary struct {
	Capabilities []capability `json:"capabilities"`
	Roles        []roleStatus `json:"roles"`
	Extractors   []extractor  `json:"extractors"`
	// ExtractorsSeenBy is whose PATH the extractors were found on: the daemon
	// when it answered (it is what ingests), else this process.
	ExtractorsSeenBy string `json:"extractorsSeenBy"`
}

// capability is one thing raglit can do, whether it is on, and what turns it on.
type capability struct {
	Name  string `json:"name"`
	On    bool   `json:"on"`
	Via   string `json:"via,omitempty"`   // what provides it when on
	Needs string `json:"needs,omitempty"` // when off: what would turn it on
	How   string `json:"how,omitempty"`   // when off: how to get that
}

// roleStatus is one model role as configured.
type roleStatus struct {
	Role     string `json:"role"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Set      bool   `json:"set"`
	// HasKey is whether a key resolves for the role's provider, inline or by
	// reference (api_key_env, api_key_file). A provider with no key is legal.
	HasKey bool `json:"hasKey"`
	// FallsBackTo names the role whose model runs this one when it is unset.
	FallsBackTo string `json:"fallsBackTo,omitempty"`
	Adds        string `json:"adds"`
}

// extractor is one external tool some file kind needs.
type extractor struct {
	Name     string   `json:"name"`
	Found    bool     `json:"found"`
	Path     string   `json:"path,omitempty"`
	Adds     string   `json:"adds"`
	Optional bool     `json:"optional"`
	Install  *install `json:"install,omitempty"` // when missing and installable
}

// install is poly-lsp-mcp's install hint: the command and what it needs.
type install struct {
	Cmd   string   `json:"cmd"`
	Needs []string `json:"needs"` // "system" for a package manager
}

const setRoles = "raglit init, or PUT /api/settings/models on the daemon"

// buildSetupSummary reads cfg and the extractors env into the summary.
func buildSetupSummary(cfg raglit.Config, env raglit.ToolEnv) setupSummary {
	found := map[string]bool{}
	for _, t := range env.Tools {
		found[t.Name] = t.Found
	}
	identity := firstNonEmpty(cfg.IdentityModel, cfg.SegmentModel, cfg.VisionModel)
	s := setupSummary{ExtractorsSeenBy: env.Who}

	caps := []capability{
		{Name: "keyword search over text, markdown and code", On: true, Via: "built in (SQLite FTS5)"},
		{Name: "semantic and hybrid search", On: cfg.EmbedModel != "", Via: cfg.EmbedModel,
			Needs: "an embedding model (embed role)", How: setRoles},
		{Name: "PDF text", On: found["pdftotext"] && found["pdftoppm"], Via: "poppler",
			Needs: "poppler (pdftotext, pdftoppm)", How: hintFor(env, "pdftotext")},
	}
	scans := capability{Name: "scanned pages and photos", Needs: "a vision model (vision role) or tesseract",
		How: setRoles + "; or " + hintFor(env, "tesseract")}
	switch {
	case cfg.VisionModel != "":
		scans.On, scans.Via = true, cfg.VisionModel
	case found["tesseract"]:
		scans.On, scans.Via = true, "tesseract (no model: plain OCR, no figure descriptions)"
	}
	caps = append(caps, scans,
		capability{Name: "figure descriptions and hard-page OCR", On: cfg.VisionModel != "", Via: cfg.VisionModel,
			Needs: "a vision model (vision role)", How: setRoles},
		capability{Name: "captions, summaries and document kinds", On: identity != "" && !cfg.NoIdentity, Via: identity,
			Needs: "a chat model (summary role, else text, else vision)", How: setRoles},
		capability{Name: "audio and video transcripts", On: cfg.AudioModel != "", Via: cfg.AudioModel,
			Needs: "a transcription model (audio role)", How: setRoles},
		capability{Name: "figure search by image", On: cfg.ImageEmbed.Model != "", Via: cfg.ImageEmbed.Model,
			Needs: "an image embedding model (image_embed in config.json)", How: "config.json image_embed {base_url, model}"},
	)
	for i := range caps {
		if caps[i].On {
			caps[i].Needs, caps[i].How = "", ""
		} else {
			caps[i].Via = ""
		}
	}
	s.Capabilities = caps

	key := func(provider string) bool { _, k, _ := cfg.Endpoint(provider); return k != "" }
	role := func(name, provider, model, falls, adds string) roleStatus {
		r := roleStatus{Role: name, Provider: provider, Model: model, Set: model != "", Adds: adds}
		if r.Set {
			r.HasKey = key(provider)
		} else {
			r.FallsBackTo = falls
		}
		return r
	}
	s.Roles = []roleStatus{
		role("embed", cfg.EmbedProvider, cfg.EmbedModel, "", "semantic and hybrid search, similar documents"),
		role("vision", cfg.VisionProvider, cfg.VisionModel, "", "OCR of scans and photos, figure descriptions"),
		role("segment", cfg.SegmentProvider, cfg.SegmentModel, "vision", "splits transcribed pages into passages"),
		role("identity", cfg.IdentityProvider, cfg.IdentityModel, "segment", "a caption, summary and kind per document"),
		role("audio", cfg.AudioProvider, cfg.AudioModel, "", "audio and video transcripts"),
	}

	for _, t := range env.Tools {
		e := extractor{Name: t.Name, Found: t.Found, Path: t.Path, Adds: t.Purpose, Optional: t.Optional}
		// A hint that is advice rather than a command (no Homebrew formula) gets
		// no install block.
		if !t.Found && (strings.HasPrefix(t.Install, "sudo apt-get ") || strings.HasPrefix(t.Install, "brew ")) {
			e.Install = &install{Cmd: t.Install, Needs: []string{"system"}}
		}
		s.Extractors = append(s.Extractors, e)
	}
	return s
}

// hintFor is a tool's install command, "" when env does not list it.
func hintFor(env raglit.ToolEnv, name string) string {
	for _, t := range env.Tools {
		if t.Name == name {
			return t.Install
		}
	}
	return ""
}

type setupOut struct {
	Body setupSummary
}

// setupOp is the daemon's summary: ITS config and ITS PATH, which are what
// ingest and search use, whatever the asking client's shell has. Host detail,
// like /api/tools: admin only when auth is on.
func setupOp(reg *raglit.Registry, home raglit.Home) func(context.Context, *struct{}) (*setupOut, error) {
	return func(ctx context.Context, _ *struct{}) (*setupOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		cfg, _, err := raglit.LoadConfig(home)
		if err != nil {
			return nil, err
		}
		env := raglit.ProbeTools()
		env.Who = fmt.Sprintf("daemon (pid %d)", os.Getpid())
		return &setupOut{Body: buildSetupSummary(cfg, env)}, nil
	}
}

// fetchDaemonSetup asks a daemon for its summary. Short timeout, no retry, as
// fetchDaemonTools: a daemon that does not answer is a finding, not a wait.
func fetchDaemonSetup(base string) (setupSummary, error) {
	var s setupSummary
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return s, fmt.Errorf("no daemon configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/setup", nil)
	if err != nil {
		return s, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("%s/api/setup: HTTP %d", base, resp.StatusCode)
	}
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

// localOrDaemonSetup is the daemon's summary when daemonURL answers, else this
// process's own (cfg, its PATH), with the daemon's URL or "".
func localOrDaemonSetup(cfg raglit.Config, daemonURL string) (setupSummary, string) {
	if daemonURL != "" {
		if s, err := fetchDaemonSetup(daemonURL); err == nil {
			return s, daemonURL
		}
	}
	env := raglit.ProbeTools()
	env.Who = "this process"
	return buildSetupSummary(cfg, env), ""
}

// buildDoctorReport is the whole `doctor --json` answer. daemonURL is "" for
// --local.
func buildDoctorReport(home raglit.Home, cfg raglit.Config, exists bool, daemonURL string) doctorReport {
	summary, daemon := localOrDaemonSetup(cfg, daemonURL)
	r := doctorReport{
		Build:        thisBuild.String(),
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
		Home:         string(home),
		Configured:   exists,
		Daemon:       daemon,
		setupSummary: summary,
	}
	if exe, err := selfPath(); err == nil {
		r.Binary = exe
	} else if p, err := exec.LookPath(os.Args[0]); err == nil {
		r.Binary = p
	}
	r.Update = newUpdateChecker().check(classifyBuild(readBuildInfo(), releaseBuild))
	return r
}

// readBuildInfo is debug.ReadBuildInfo for classifyBuild: nil when there is none.
func readBuildInfo() *debug.BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return bi
}
