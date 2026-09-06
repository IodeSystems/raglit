package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iodesystems/raglit"
)

//go:embed demodata/*.md
var demoDocs embed.FS

// runDemo is a self-contained, offline tour: it writes a small embedded corpus
// to a temp home, enqueues the docs as file:// URLs (lazy), drains the queue one
// job at a time — printing index_status after each so the pending count, rate,
// and ETAs are visible — then runs a search. No LLM/config needed.
func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	keep := fs.Bool("keep", false, "keep the temp demo home instead of deleting it")
	fs.Parse(args)

	dir, err := os.MkdirTemp("", "raglit-demo-")
	if err != nil {
		return err
	}
	if *keep {
		fmt.Printf("demo home: %s (kept)\n", dir)
	} else {
		defer os.RemoveAll(dir)
	}
	home := raglit.Home(filepath.Join(dir, "home"))

	// Materialize the embedded corpus so it can be ingested by file:// URL.
	corpus := filepath.Join(dir, "corpus")
	if err := os.MkdirAll(corpus, 0o755); err != nil {
		return err
	}
	entries, _ := demoDocs.ReadDir("demodata")
	var urls []string
	for _, e := range entries {
		b, _ := demoDocs.ReadFile("demodata/" + e.Name())
		p := filepath.Join(corpus, e.Name())
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		urls = append(urls, "file://"+p)
	}

	store, err := raglit.OpenHome(home)
	if err != nil {
		return err
	}
	defer store.Close()

	fmt.Printf("raglit demo — %d docs into %s\n\n", len(urls), home)

	// LAZY: enqueue everything at once (instant), then a worker drains it.
	for _, u := range urls {
		id, err := store.Enqueue(u, "")
		if err != nil {
			return err
		}
		fmt.Printf("queued #%d  %s\n", id, u)
	}

	fmt.Print("\ndraining queue (status after each job):\n\n")
	// A tiny artificial delay per job so the queue's rate/ETA are observable —
	// real text ingestion is instant.
	w := &raglit.Worker{Store: store, Fetcher: func(ctx context.Context, url string) (raglit.Fetched, error) {
		time.Sleep(400 * time.Millisecond)
		return raglit.Fetch(ctx, url)
	}}
	for {
		did, err := w.ProcessOne(context.Background())
		if err != nil {
			return err
		}
		if !did {
			break
		}
		printStatus(store)
		fmt.Println()
	}

	for _, q := range []string{"how does token refresh work", "rolling back a bad release"} {
		fmt.Printf("search %q:\n", q)
		hits, err := store.Search(q, 2)
		if err != nil {
			return err
		}
		for i, h := range hits {
			fmt.Printf("  %d. [%.2f] %s — %s\n", i+1, h.Score, h.Title, clip(oneLine(h.Text), 80))
		}
		fmt.Println()
	}
	fmt.Println("that's the lazy-ingest + status + search loop. `raglit serve` exposes")
	fmt.Println("the same as MCP tools: ingest, index_status, search.")
	return nil
}
