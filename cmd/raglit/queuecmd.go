package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	cfg, _, _ := raglit.LoadConfig(home)
	w := &raglit.Worker{Store: store}
	// Deterministic text fragmenter params (config-or-default), with the fragment
	// ceiling capped by the embed model's probed input limit.
	w.Frag = raglit.FragConfig{
		Window:       cfg.FragWindow,
		Stride:       cfg.FragStride,
		Floor:        cfg.FragFloor,
		EmbedLimit:   cfg.EmbedLimitChars,
		FigurePrompt: raglit.FigurePromptVersion(),
	}
	if *lf.visionModel != "" {
		client := lf.visionClient()
		w.OCR = raglit.NewOCR(client)
		attachCheapOCR(w.OCR, home)
		// Only used when a page escalates to the VLM (llm-seg); text never does.
		w.Segmenter = raglit.NewSegmenter(client)
	}
	// Cross-index pool (daemon only): key ingest work by (recipe, file). The
	// recipe is the models + config that shape the output — including the
	// fragmenter (§5) — so alt models OR a stride change reprocess.
	if pool != nil {
		w.Pool = pool
		fw, fs, ff := raglit.ResolveFragParams(cfg.FragWindow, cfg.FragStride, cfg.FragFloor, cfg.EmbedLimitChars)
		recipe := fmt.Sprintf("seg=%s|emb=%s|ocr=%s|frag=overlap,w=%d,s=%d,f=%d|fig=%d",
			*lf.visionModel, *lf.embedModel, cfg.OCR.CheapEngine, fw, fs, ff, raglit.FigurePromptVersion())
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
		idx, err := nsWriteIndex(ns, projectShared(homeOf), resolveIndexName(fs.Lookup("index").Value.String(), homeOf))
		if err != nil {
			return err
		}
		return daemonIngest(dURL, targets, idx, *title)
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
		// No project to resolve. Show what EXISTS instead of only naming a file to
		// edit — this is the command people run to find out where they are.
		return printIndexDirectory(err)
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

// printIndexDirectory is what `raglit status` shows when it has no project to
// resolve — i.e. run anywhere outside a configured project.
//
// Refusing with "no project name — set project in ..." is correct and useless:
// it names a file to edit without saying what exists, so the indexes the daemon
// is holding, and their paths, are undiscoverable from the command whose whole
// job is to report state. A tool that knows the answer should say it.
func printIndexDirectory(reason error) error {
	root := raglit.DefaultRoot()
	fmt.Printf("not in a project — no index selected.\n")
	if reason != nil {
		fmt.Printf("  (%s)\n", firstSentence(reason.Error()))
	}
	fmt.Printf("\nstorage root: %s\n", root)

	// daemon.json records where a daemon WAS; it survives a kill, so believing it
	// would report a dead daemon as running. Probe.
	if st, recorded := readDaemonState(root); !recorded {
		fmt.Println("daemon:       not running")
	} else if base := "http://" + st.Addr; daemonHealthy(base) {
		fmt.Printf("daemon:       %s  (pid %d, since %s)\n", base, st.PID, st.StartedAt)
	} else {
		fmt.Printf("daemon:       not running  (stale %s records pid %d at %s)\n",
			filepath.Base(daemonStatePath(root)), st.PID, st.Addr)
	}

	// The indexes themselves, from disk, so this works with the daemon down.
	dir := filepath.Join(root, "indexes")
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		fmt.Printf("\nno indexes under %s\n", dir)
		fmt.Println("`raglit init` in a project directory creates one.")
		return nil
	}
	fmt.Printf("\n%-34s %-9s %-11s %s\n", "INDEX", "DOCS", "FRAGMENTS", "PATH")
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		p := filepath.Join(dir, name, "index.sqlite")
		docs, frags := indexCounts(p)
		fmt.Printf("%-34s %-9s %-11s %s\n", name, docs, frags, p)
	}
	fmt.Println("\nAn index is named <project>__<index>. To act on one:")
	fmt.Println("  cd into the project (its .raglit/config.json names it), or")
	fmt.Println("  raglit status --project <project> [--index <index>]")
	return nil
}

// indexCounts reads an index's totals directly, so the directory listing works
// whether or not the daemon is up. Unreadable is reported as "?" rather than 0 —
// zero is a claim about the index, "?" is a claim about our access to it.
func indexCounts(path string) (string, string) {
	s, err := raglit.Open(path)
	if err != nil {
		return "?", "?"
	}
	defer s.Close()
	st, err := s.IndexStatus()
	if err != nil {
		return "?", "?"
	}
	return fmt.Sprint(st.Documents), fmt.Sprint(st.Fragments)
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}
