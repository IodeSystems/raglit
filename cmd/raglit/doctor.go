package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// runDoctor reports OCR readiness: whether the configured cheap engine is
// runnable, whether the vision endpoint is reachable, and which cascade tiers
// are therefore available. It is the answer to "the user recalled tesseract
// being hard to install" — a one-shot check with the exact install hint.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	homeFlag := fs.String("home", "", "config home dir (default: nearest ./.raglit, else ~/local/raglit)")
	daemonFlag := fs.String("daemon", "", "ask this daemon what IT can see (default $RAGLIT_DAEMON, config, else the shared daemon)")
	localOnly := fs.Bool("local", false, "report only this shell's environment; do not ask the daemon")
	fs.Parse(args)
	home := raglit.DiscoverHome()
	if *homeFlag != "" {
		home = raglit.Home(*homeFlag)
	}

	cfg, exists, err := raglit.LoadConfig(home)
	if err != nil {
		return err
	}
	fmt.Printf("raglit doctor — OCR readiness\n  home:   %s\n", home)
	if !exists {
		fmt.Println("  config: NOT initialized — run `raglit init`")
	}

	// Vision (VLM fallback) tier.
	fmt.Println("\nvision (VLM) tier:")
	visionOK := cfg.VisionModel != ""
	if !visionOK {
		fmt.Println("  ✗ no vision_model configured — the cascade cannot fall back to a VLM")
	} else {
		fmt.Printf("  model:    %s\n  endpoint: %s\n", cfg.VisionModel, cfg.BaseURL)
		if ok, detail := pingEndpoint(cfg.BaseURL); ok {
			fmt.Printf("  ✓ endpoint reachable (%s)\n", detail)
		} else {
			fmt.Printf("  ✗ endpoint unreachable: %s\n", detail)
		}
	}

	// Cheap (first-pass) tier.
	fmt.Println("\ncheap (first-pass) tier:")
	cheapOK := false
	switch strings.ToLower(strings.TrimSpace(cfg.OCR.CheapEngine)) {
	case "", "none":
		fmt.Println("  · disabled (cheap_engine=none) — every page uses the VLM")
	case "tesseract":
		bin := cfg.OCR.TesseractBin
		if bin == "" {
			bin = "tesseract"
		}
		if v, e := tesseractVersion(bin); e == nil {
			fmt.Printf("  ✓ tesseract: %s (%s)\n", v, bin)
			cheapOK = true
		} else {
			fmt.Printf("  ✗ tesseract not runnable (%s): %v\n", bin, e)
			fmt.Println("     install:  sudo apt-get install tesseract-ocr tesseract-ocr-eng")
			fmt.Println("     no sudo:  deb-extract into a prefix — recipe in raglit/plan/ocr-mcp.md")
		}
	case "paddle", "paddleocr":
		if _, e := raglit.BuildPageEngine(cfg.OCR); e != nil {
			fmt.Printf("  ✗ %v\n", e)
		} else if ok, detail := pingEndpoint(cfg.OCR.PaddleURL); ok {
			fmt.Printf("  ✓ paddleocr reachable at %s (%s)\n", cfg.OCR.PaddleURL, detail)
			cheapOK = true
		} else {
			fmt.Printf("  ✗ paddleocr unreachable at %s: %s\n", cfg.OCR.PaddleURL, detail)
			fmt.Println("     run a PaddleOCR sidecar exposing POST /ocr (docker), then set ocr.paddle_url")
		}
	default:
		fmt.Printf("  ✗ unknown cheap_engine %q (want none|tesseract|paddleocr)\n", cfg.OCR.CheapEngine)
	}

	// Format extractors — IN THE PROCESS THAT RUNS THEM.
	//
	// This used to probe the shell doctor was typed in and print a tick per tool.
	// That is the wrong process: ingest runs in the daemon, and on 2026-08-09
	// every .docx in a corpus failed with "pandoc not installed" while this
	// command printed "✓ pandoc". Both were true. systemd --user does not
	// inherit a login shell's PATH, pandoc was in ~/local/bin, and the green tick
	// sent the search for the fault somewhere it could not be.
	shellEnv := raglit.ProbeTools()
	shellEnv.Who = "this shell"
	daemonEnv, daemonWhere, daemonErr := raglit.ToolEnv{}, "", error(nil)
	if !*localOnly {
		daemonWhere = resolveDaemon(*daemonFlag, func() raglit.Home { return home })
		if daemonWhere == "" {
			daemonWhere = defaultDaemonURL
		}
		daemonEnv, daemonErr = fetchDaemonTools(daemonWhere)
	}

	// The daemon's answer is the one that decides whether an ingest works, so it
	// is the one reported. The shell's is kept only to explain a difference.
	report, authority := shellEnv, "this shell"
	if daemonErr == nil && len(daemonEnv.Tools) > 0 {
		report, authority = daemonEnv, daemonEnv.Who
	}
	fmt.Printf("\nformat extractors — as seen by %s:\n", authority)
	if daemonErr != nil && !*localOnly {
		fmt.Printf("  (no daemon at %s: %v — reporting THIS SHELL, which is not what ingests)\n",
			daemonWhere, daemonErr)
	}
	for _, t := range report.Tools {
		switch {
		case t.Found:
			fmt.Printf("  ✓ %-10s %-34s %s\n", t.Name, t.Purpose, t.Path)
		case t.Optional:
			fmt.Printf("  · %-10s %s — MISSING (optional)\n", t.Name, t.Purpose)
			fmt.Printf("     install:  %s\n", t.Install)
		default:
			fmt.Printf("  ✗ %-10s %s — MISSING\n", t.Name, t.Purpose)
			fmt.Printf("     install:  %s\n", t.Install)
		}
	}
	fmt.Println("  ✓ .xlsx      read natively (stdlib zip+XML, no external tool needed)")

	// A disagreement is the actionable finding: the tool IS installed and the
	// process that needs it cannot see it. Printing both PATHs is the fix.
	if daemonErr == nil && len(daemonEnv.Tools) > 0 {
		if diffs := shellEnv.Disagreements(daemonEnv); len(diffs) > 0 {
			fmt.Println("\n  ⚠ this shell and the daemon DISAGREE:")
			for _, d := range diffs {
				fmt.Printf("      %s\n", d)
			}
			onlyShell, onlyDaemon := shellEnv.PathDiff(daemonEnv)
			if len(onlyShell) > 0 {
				fmt.Printf("      PATH only the shell has:  %s\n", strings.Join(onlyShell, " "))
			}
			if len(onlyDaemon) > 0 {
				fmt.Printf("      PATH only the daemon has: %s\n", strings.Join(onlyDaemon, " "))
			}
			fmt.Println("      fix: give the daemon the PATH, e.g. a systemd drop-in")
			fmt.Println("           ~/.config/systemd/user/raglit.service.d/path.conf")
			fmt.Println("           [Service] / Environment=PATH=...")
			fmt.Println("      (a drop-in, not an edit: `raglit service install` regenerates the unit)")
		}
	}

	// Verdict — which tiers are live.
	fmt.Println("\nverdict:")
	switch {
	case cheapOK && visionOK:
		fmt.Println("  ✓ full cascade: cheap first-pass → gibberish gate → VLM fallback")
	case visionOK:
		fmt.Println("  ✓ VLM-only: every page transcribed by the vision model (no cheap tier)")
	case cheapOK:
		fmt.Println("  ⚠ cheap-only: clean pages OK, but a gibberish page has no VLM to escalate to")
	default:
		fmt.Println("  ✗ OCR unavailable: configure a vision_model and/or a cheap_engine")
	}
	return nil
}

// tesseractVersion runs `<bin> --version` and returns its first line (e.g.
// "tesseract 5.3.4"), or an error if the binary is missing / not runnable.
func tesseractVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]), nil
}

// pingEndpoint does a quick reachability GET. For an OpenAI base URL (…/v1) it
// hits …/v1/models; otherwise it hits the URL itself. A non-5xx status counts as
// reachable — a 401/404 still proves the service is up.
func pingEndpoint(base string) (ok bool, detail string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return false, "no URL configured"
	}
	url := base
	if strings.HasSuffix(base, "/v1") {
		url = base + "/models"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

// fetchDaemonTools asks a running daemon what external tools IT can see.
//
// Short timeout and no retry: this is a diagnostic, and "the daemon did not
// answer" is itself a finding worth printing rather than waiting on.
func fetchDaemonTools(base string) (raglit.ToolEnv, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return raglit.ToolEnv{}, fmt.Errorf("no daemon configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tools", nil)
	if err != nil {
		return raglit.ToolEnv{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return raglit.ToolEnv{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// An older daemon has no /api/tools. Say so precisely: the check cannot
		// be performed, which is different from the tools being absent.
		return raglit.ToolEnv{}, fmt.Errorf("daemon predates this check (no /api/tools) — restart it on this build")
	}
	if resp.StatusCode >= 300 {
		return raglit.ToolEnv{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Body struct {
			Env raglit.ToolEnv `json:"env"`
		} `json:"body"`
		Env raglit.ToolEnv `json:"env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return raglit.ToolEnv{}, err
	}
	if len(body.Env.Tools) > 0 {
		return body.Env, nil
	}
	return body.Body.Env, nil
}
