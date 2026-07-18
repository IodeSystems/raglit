package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/iodesystems/raglit"
)

// buildWorker wires a Worker with OCR (PDF) + LLM segmentation (text/code) when a
// model is configured; text-window sizing is resolved lazily on the first text
// job (probing + caching the model's context). No model → offline blank-line text
// + PDF-fails-gracefully.
func buildWorker(store *raglit.Store, lf *llmFlags, home raglit.Home) *raglit.Worker {
	w := &raglit.Worker{Store: store}
	if *lf.visionModel != "" {
		client := lf.visionClient()
		w.OCR = raglit.NewOCR(client)
		w.Segmenter = raglit.NewSegmenter(client)
		// Window from --context-tokens if given, else config-or-smart-default.
		if *lf.contextTokens > 0 {
			w.WindowChars = raglit.WindowCharsFor(*lf.contextTokens)
		} else {
			w.WindowChars = raglit.WindowCharsForHome(home)
		}
	}
	return w
}

// runIngest enqueues URLs for lazy ingestion. With --now it also drains the
// queue synchronously (fetch + index) instead of leaving it for a serve worker.
func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	title := fs.String("title", "", "document title (single-URL convenience)")
	now := fs.Bool("now", false, "also process the queue now (don't wait for a serve worker)")
	embed := fs.Bool("embed", false, "with --now: embed ingested fragments")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("ingest: no URLs given (file://<path> or http(s)://...)")
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	for _, url := range fs.Args() {
		id, err := store.Enqueue(url, *title)
		if err != nil {
			return err
		}
		fmt.Printf("queued #%d  %s\n", id, url)
	}

	if *now {
		lf.resolve(homeOf())
		if *embed {
			if err := lf.requireEmbed(); err != nil {
				return err
			}
			store.SetEmbedder(lf.embedder())
		}
		n, err := buildWorker(store, lf, homeOf()).Drain(context.Background())
		if err != nil {
			return err
		}
		fmt.Printf("processed %d job(s)\n", n)
		printStatus(store)
	}
	return nil
}

// runWork drains the queue once (fetch + index all pending), then exits — for a
// cron/one-shot worker without a long-running serve.
func runWork(args []string) error {
	fs := flag.NewFlagSet("work", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	embed := fs.Bool("embed", false, "embed ingested fragments")
	fs.Parse(args)
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	lf.resolve(homeOf())
	if *embed {
		if err := lf.requireEmbed(); err != nil {
			return err
		}
		store.SetEmbedder(lf.embedder())
	}
	n, err := buildWorker(store, lf, homeOf()).Drain(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("processed %d job(s)\n", n)
	printStatus(store)
	return nil
}

// runStatus prints the index + queue status.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	openStore, _ := addStoreFlags(fs)
	fs.Parse(args)
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	printStatus(store)
	return nil
}

// printStatus renders a Status to stdout.
func printStatus(store *raglit.Store) {
	st, err := store.IndexStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return
	}
	fmt.Printf("index: %d document(s), %d fragment(s)\n", st.Documents, st.Fragments)
	fmt.Printf("jobs:  done=%d running=%d pending=%d failed=%d", st.Done, st.Running, st.Pending, st.Failed)
	if st.RatePerMin > 0 {
		fmt.Printf("  (%.1f/min)", st.RatePerMin)
	}
	fmt.Println()
	for _, it := range st.Items {
		eta := "eta n/a"
		if it.ETASeconds > 0 {
			eta = fmt.Sprintf("eta ~%.0fs", it.ETASeconds)
		}
		fmt.Printf("  %-8s #%d %s  (%s)\n", it.State, it.ID, it.URL, eta)
	}
}
