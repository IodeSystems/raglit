package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
)

// expandIngestTargets turns ingest args into a flat list of ingestable targets:
// a URL (file://, http(s)://) passes through; a local directory is walked for
// text/PDF files; a local file becomes its absolute path. So `ingest ./repo`
// queues every source file under repo.
func expandIngestTargets(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		if strings.Contains(a, "://") { // a URL
			out = append(out, a)
			continue
		}
		fi, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			err = filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && (isText(p) || isPDF(p)) {
					abs, _ := filepath.Abs(p)
					out = append(out, abs)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			abs, _ := filepath.Abs(a)
			out = append(out, abs)
		}
	}
	return out, nil
}

// buildWorker wires a Worker with OCR (PDF) + LLM segmentation (text/code) when a
// model is configured; text-window sizing is resolved lazily on the first text
// job (probing + caching the model's context). No model → offline blank-line text
// + PDF-fails-gracefully.
func buildWorker(store *raglit.Store, lf *llmFlags, home raglit.Home, pool *raglit.Pool) *raglit.Worker {
	w := &raglit.Worker{Store: store}
	if *lf.visionModel != "" {
		client := lf.visionClient()
		w.OCR = raglit.NewOCR(client)
		attachCheapOCR(w.OCR, home)
		w.Segmenter = raglit.NewSegmenter(client)
		// Window from --context-tokens if given, else config-or-smart-default.
		if *lf.contextTokens > 0 {
			w.WindowChars = raglit.WindowCharsFor(*lf.contextTokens)
		} else {
			w.WindowChars = raglit.WindowCharsForHome(home)
		}
	}
	// Cross-index pool (daemon only): key ingest work by (recipe, file). The
	// recipe is the models + config that shape the output, so alt models reprocess.
	if pool != nil {
		w.Pool = pool
		cfg, _, _ := raglit.LoadConfig(home)
		recipe := fmt.Sprintf("seg=%s|emb=%s|ocr=%s|win=%d", *lf.visionModel, *lf.embedModel, cfg.OCR.CheapEngine, w.WindowChars)
		w.RecipeHash = raglit.HashHex([]byte(recipe))
	}
	return w
}

// runIngest enqueues URLs for lazy ingestion. With --now it also drains the
// queue synchronously (fetch + index) instead of leaving it for a serve worker.
func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	client := addClientFlags(fs) // --daemon + --embedded
	title := fs.String("title", "", "document title (single-URL convenience)")
	now := fs.Bool("now", false, "also process the queue now (don't wait for a serve worker)")
	embed := fs.Bool("embed", false, "with --now: embed ingested fragments")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("ingest: nothing given (a folder, file, file://<path>, or http(s)://...)")
	}

	targets, err := expandIngestTargets(fs.Args())
	if err != nil {
		return err
	}

	// Default: hand off to the shared daemon (auto-started if needed). --embedded
	// or --db opens the index in-process instead.
	dURL, ns, err := client(homeOf, fs.Lookup("db").Value.String() != "")
	if err != nil {
		return err
	}
	if dURL != "" {
		return daemonIngest(dURL, targets, nsIndex(ns, resolveIndexName(fs.Lookup("index").Value.String(), homeOf)), *title)
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	for _, u := range targets {
		id, err := store.Enqueue(u, *title)
		if err != nil {
			return err
		}
		fmt.Printf("queued #%d  %s\n", id, u)
	}
	fmt.Printf("queued %d item(s)\n", len(targets))

	if *now {
		lf.resolve(homeOf())
		if *embed {
			if err := lf.requireEmbed(); err != nil {
				return err
			}
			store.SetEmbedder(lf.embedder())
		}
		n, err := buildWorker(store, lf, homeOf(), nil).Drain(context.Background())
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
	n, err := buildWorker(store, lf, homeOf(), nil).Drain(context.Background())
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
	openStore, homeOf := addStoreFlags(fs)
	client := addClientFlags(fs) // --daemon + --embedded
	fs.Parse(args)
	dURL, ns, err := client(homeOf, fs.Lookup("db").Value.String() != "")
	if err != nil {
		return err
	}
	if dURL != "" {
		return daemonStatusPrint(dURL, nsReadSelector(ns, projectShared(homeOf), fs.Lookup("index").Value.String()))
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	printStatus(store)
	return nil
}

// printStatus renders a store's Status to stdout.
func printStatus(store *raglit.Store) {
	st, err := store.IndexStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return
	}
	renderStatus(st)
}

// renderStatus prints a Status value (shared by the local + daemon paths).
func renderStatus(st raglit.Status) {
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
