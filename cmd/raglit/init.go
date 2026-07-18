package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/agentkit/llm"
	"github.com/iodesystems/raglit"
)

// runInit is the setup wizard. It writes <home>/config.json: an OpenAI-standard
// base URL + token, then a vision model (image in → text, for OCR) and an
// embedding model (text in → vector). It queries the endpoint's /v1/models and
// lets you pick from the list; if that fails, you type ids by hand.
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	homeFlag := fs.String("home", "", "index home dir (default $RAGLIT_HOME or ~/local/raglit)")
	fs.Parse(args)

	home := raglit.DefaultHome()
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
		fmt.Printf("\n%d models available:\n", len(models))
		for i, m := range models {
			fmt.Printf("  %2d) %s\n", i+1, m)
		}
		vision = pick(r, models, "\nvision model (image in → text, for PDF OCR)")
		embed = pick(r, models, "embedding model (text in → vector, for --embed)")
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

	if err := raglit.SaveConfig(home, raglit.Config{
		BaseURL: base, APIKey: key, VisionModel: vision, EmbedModel: embed, ContextTokens: ctxTokens,
	}); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s\n", home.ConfigPath())
	fmt.Println("ready: raglit index <files>  ·  raglit search \"query\"  ·  raglit serve")
	return nil
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

// pick lets the user choose from options by number OR type an id directly (so a
// model not in the list still works).
func pick(r *bufio.Reader, options []string, label string) string {
	in := ask(r, label+" (number or id)", "")
	if n, err := strconv.Atoi(in); err == nil && n >= 1 && n <= len(options) {
		return options[n-1]
	}
	return in
}

// fetchModels GETs <base>/v1/models and returns the sorted model ids.
func fetchModels(ctx context.Context, base, key string) ([]string, error) {
	u := strings.TrimRight(base, "/")
	if !strings.HasSuffix(u, "/v1") {
		u += "/v1"
	}
	u += "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
