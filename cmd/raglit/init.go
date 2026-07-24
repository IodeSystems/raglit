package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/agentkit/llm"
	"github.com/iodesystems/raglit"
)

// runInit is the setup wizard. By default it writes a PROJECT-LOCAL home —
// ./.raglit/ in the current directory — so a repo/sub-project owns its own
// index and config; every command run anywhere in the tree discovers it by
// walking up (see raglit.DiscoverHome). --home overrides the location.
//
// config.json is OpenAI-standard: a base URL + token, then a vision model
// (image in → text, for OCR) and an embedding model (text in → vector). init
// queries the endpoint's /v1/models; if the server reports capabilities
// (corrallm-class), each pick list is filtered to models that fit the role —
// image-in for vision, embeddings for --embed. A plain OpenAI server shows the
// full list. If the query fails, you type ids by hand. On success it prints the
// MCP server setup plus the ingest/search commands for reference.
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	homeFlag := fs.String("home", "", "home dir (default: ./.raglit in the current directory)")
	fs.Parse(args)

	home := raglit.Home(raglit.ProjectHomeName) // ./.raglit
	if *homeFlag != "" {
		home = raglit.Home(*homeFlag)
	}

	r := bufio.NewReader(os.Stdin)
	fmt.Printf("raglit setup — configuring %s\n\n", home.ConfigPath())
	if raglit.Inited(home) {
		if !strings.HasPrefix(strings.ToLower(ask(r, "config already exists; overwrite?", "n")), "y") {
			fmt.Println("keeping existing config.")
			return nil
		}
	}

	base := ask(r, "OpenAI-compatible base URL", "https://api.openai.com/v1")
	key := ask(r, "API key / token", "")

	fmt.Println("\nquerying available models…")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := fetchModels(ctx, base, key)

	var vision, embed string
	if err == nil && len(models) > 0 {
		enriched := hasCapabilities(models)
		if enriched {
			fmt.Printf("\n%d models (capabilities reported — filtering per role)\n", len(models))
		} else {
			fmt.Printf("\n%d models available\n", len(models))
		}
		vision = chooseModel(r, models, enriched, "vision", "vision model (image in → text, for PDF OCR)")
		embed = chooseModel(r, models, enriched, "embed", "embedding model (text in → vector, for --embed)")
	} else {
		if err != nil {
			fmt.Fprintf(os.Stderr, "  couldn't list models (%v) — enter ids manually\n", err)
		}
		vision = ask(r, "vision model id (image in → text)", "")
		embed = ask(r, "embedding model id (text in → vector)", "")
	}

	// Context window. Blank → a smart default (fine for any large-context model,
	// since the window is output-reliability-capped). A number sets it. "probe"
	// auto-detects by blowing the limit — only works on servers that REJECT an
	// over-long prompt (tolerant proxies like bonsai don't, so just leave it
	// blank there).
	var ctxTokens int
	ans := ask(r, "context window tokens (blank = smart default; a number; or 'probe')", "")
	switch {
	case ans == "":
		// smart default
	case strings.EqualFold(ans, "probe"):
		fmt.Println("probing context (sends growing prompts until one is rejected)…")
		pctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		n, perr := llm.NewClient(base, key, vision).DiscoverContext(pctx)
		cancel()
		if perr != nil {
			fmt.Fprintf(os.Stderr, "  probe failed (%v) — using smart default\n", perr)
		} else {
			ctxTokens = n
			fmt.Printf("  discovered %d tokens\n", n)
		}
	default:
		fmt.Sscanf(ans, "%d", &ctxTokens)
	}

	// Project name — namespaces this project's indexes on the shared daemon, so
	// two projects both using "default" don't collide. Required to start a
	// daemon-routed client. Default: the project directory's name.
	project := ask(r, "project name (namespaces this project's indexes on the shared daemon)", defaultProjectName(home))

	// Shared namespaces — other projects this one also SEARCHES (e.g. a "shared"
	// project holding ~/doc). Comma-separated; blank = fully isolated.
	shared := splitCSV(ask(r, "shared namespaces to also search (comma-separated, blank = none)", ""))

	// Default index — the one commands use when no --index is given.
	defIndex := ask(r, "default index name", "default")

	if err := raglit.SaveConfig(home, raglit.Config{
		BaseURL: base, APIKey: key, VisionModel: vision, EmbedModel: embed,
		ContextTokens: ctxTokens, DefaultIndex: defIndex, Project: project, Shared: shared,
	}); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s\n", home.ConfigPath())
	printPostInit(home)
	return nil
}

// printPostInit prints, after a successful init, the MCP server setup (so an
// agent can reach this index) and the ingest/search commands for reference. The
// MCP snippet pins an absolute --home so it works regardless of the client's
// working directory; the CLI examples rely on walk-up discovery of ./.raglit,
// so they need no --home when run inside the project.
func printPostInit(home raglit.Home) {
	abs, err := filepath.Abs(string(home))
	if err != nil {
		abs = string(home)
	}

	type mcpServer struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	srv := mcpServer{Command: "raglit", Args: []string{"serve", "--home", abs}}
	oneLine, _ := json.Marshal(srv)
	full, _ := json.MarshalIndent(
		map[string]any{"mcpServers": map[string]any{"raglit": srv}}, "  ", "  ")

	fmt.Print("\nMCP server (stdio) — expose this index to an agent:\n\n")
	fmt.Printf("  Claude Code:\n    claude mcp add-json raglit '%s'\n\n", oneLine)
	fmt.Printf("  Or add to .mcp.json (any MCP client):\n  %s\n", full)
	fmt.Print("\n  Tools: search · ingest · index_status · list_indexes · ocr\n")

	fmt.Print("\nreference (run anywhere in this project — raglit finds ./.raglit):\n")
	fmt.Println("  ingest:  raglit ingest <FILE|DIR|URL>...      queue + index (lazy)")
	fmt.Println("  index:   raglit index  <FILE|DIR>...          index now")
	fmt.Println("  search:  raglit search \"your query\"           BM25 (add --mode hybrid with --embed)")
	fmt.Println("  status:  raglit status                        index + queue state")
	fmt.Println("  doctor:  raglit doctor                        OCR / extractor readiness")
}

// defaultProjectName derives a sensible project name from the home's location:
// the project directory's basename (the parent of a ./.raglit home, else the
// home itself). Falls back to "project".
func defaultProjectName(home raglit.Home) string {
	abs, err := filepath.Abs(string(home))
	if err != nil {
		abs = string(home)
	}
	if filepath.Base(abs) == filepath.Base(raglit.ProjectHomeName) { // ".raglit"
		abs = filepath.Dir(abs)
	}
	name := raglit.NormalizeIndexName(filepath.Base(abs))
	if name == "" || name == "default" {
		return "project"
	}
	return name
}

// splitCSV splits a comma-separated reply into trimmed, non-empty entries (nil
// when blank).
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ask prompts with an optional default and returns the trimmed reply (or def).
func ask(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// chooseModel prints a numbered model pick list for role and returns the chosen
// id. When the server reports capabilities, the list is filtered to models that
// fit the role (image-in for vision, embeddings for embed); if the filter would
// empty the list it falls back to the full catalog. The default (Enter) is the
// best-ranked model; you can also type a number or an id not shown.
func chooseModel(r *bufio.Reader, all []modelInfo, enriched bool, role, label string) string {
	if len(all) == 0 {
		return ask(r, label+" id", "")
	}
	shown := all
	if enriched {
		if f := filterByRole(all, role); len(f) > 0 {
			shown = f
		}
	}
	fmt.Printf("\n%s:\n", label)
	for i, m := range shown {
		fmt.Printf("  %2d) %s\n", i+1, modelLabel(m))
	}
	in := ask(r, "  choose (number or id)", shown[0].ID)
	if n, err := strconv.Atoi(in); err == nil && n >= 1 && n <= len(shown) {
		return shown[n-1].ID
	}
	return in
}
