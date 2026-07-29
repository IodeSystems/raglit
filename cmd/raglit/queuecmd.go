package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// expandIngestTargets turns ingest args into a flat list of ingestable targets:
// a URL (file://, http(s)://) passes through; a local directory is walked for
// every file raglit can read (ClassifyDoc decides); a local file becomes its
// absolute path and is queued whatever it is. So `ingest ./repo` queues every
// readable file under repo.
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
				// ClassifyDoc is the single authority on what raglit can read.
				// This walk used to test isText||isPDF, which silently skipped
				// every office format and every image the extractor handles — a
				// directory of .docx or scanned .tif enqueued nothing and looked
				// like it had been covered. A format added to the extractor must
				// become discoverable in the same change, not the next one.
				if !d.IsDir() && raglit.ClassifyDoc(p, "") != raglit.KindUnknown {
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
	// The embed model's input limit is DISCOVERED, not configured: it is a fact
	// about the endpoint, and leaving it at zero means "no cap", which is how
	// fragments came to be sized by taste with nothing checking them.
	embedLimit := cfg.EmbedLimitChars
	if *lf.embedModel != "" {
		embedLimit = store.EmbedLimitChars(context.Background(),
			raglit.NewEmbedder(lf.embedClientForProbe(), *lf.embedModel), cfg.EmbedLimitChars)
	}
	w.Frag = raglit.FragConfig{
		Window:       cfg.FragWindow,
		Stride:       cfg.FragStride,
		Floor:        cfg.FragFloor,
		EmbedLimit:   embedLimit,
		FigurePrompt: raglit.FigurePromptVersion(),
	}
	if *lf.visionModel != "" {
		client := lf.visionClient()
		// Printed once per index worker so the effective retry policy is visible
		// in the log. Without it, "5xx attempt 2/5" in a log is ambiguous between
		// "the cap was not raised" and "this is not the client you think it is".
		log.Printf("raglit: ocr client model=%s 5xx-attempts=%d", *lf.visionModel, client.Retry5xxAttempts)
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

// runRetry requeues failed ingest jobs.
//
// The case this exists for: a transient outage — the vision model unloaded
// during a restart — failed 72 PDFs in one window with "pdf ingest needs a
// vision model". Nothing surfaced `RetryJob`, so recovering meant reading URLs
// out of SQLite by hand and feeding them back to `ingest`.
//
// A vanished path is SKIPPED, not fatal. Failed rows outlive renames, and
// `ingest` aborts on the first missing file — one stale path in a batch of 122
// killed the whole retry. Reporting the skips is the point: after a rename
// sweep, "11 gone" is the answer, not an error.
func runRetry(args []string) error {
	fs := flag.NewFlagSet("retry", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	client := addClientFlags(fs) // --daemon + --embedded + --project
	dryRun := fs.Bool("dry-run", false, "list what would be requeued, change nothing")
	state := fs.String("state", "error", "which state to requeue: error|done")
	limit := fs.Int("limit", 0, "cap how many are requeued (0 = all)")
	match := fs.String("match", "", "only jobs whose error contains this substring")
	fs.Parse(args)

	store, where, err := openQueueStore(openStore, homeOf, client,
		fs.Lookup("db").Value.String() != "", fs.Lookup("index").Value.String())
	if err != nil {
		return err
	}
	defer store.Close()
	// Which index is this? Printed because its absence hid the bug: `status` is
	// daemon-routed and reported 131 failures while `retry` opened the
	// project-local home and reported 21, and nothing on either output said they
	// were different indexes.
	fmt.Printf("index: %s\n", where)

	jobs, err := store.Jobs(*state, 100000)
	if err != nil {
		return err
	}

	var requeued, missing, filtered int
	for _, j := range jobs {
		if *match != "" && !strings.Contains(j.Error, *match) {
			filtered++
			continue
		}
		if p := localPath(j.URL); p != "" {
			if _, err := os.Stat(p); err != nil {
				missing++
				fmt.Printf("  gone     %s\n", p)
				continue
			}
		}
		if *limit > 0 && requeued >= *limit {
			break
		}
		if *dryRun {
			fmt.Printf("  would    #%d %s\n", j.ID, j.URL)
			requeued++
			continue
		}
		if err := store.RetryJob(j.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  failed   #%d: %v\n", j.ID, err)
			continue
		}
		requeued++
	}

	verb := "requeued"
	if *dryRun {
		verb = "would requeue"
	}
	fmt.Printf("%s %d · %d gone (skipped)", verb, requeued, missing)
	if filtered > 0 {
		fmt.Printf(" · %d filtered out by --match", filtered)
	}
	fmt.Println()
	if missing > 0 {
		fmt.Println("  gone = the row outlived the file, usually a rename. Nothing to do.")
	}
	return nil
}

// localPath returns the filesystem path a job URL refers to, or "" when the job
// is not a local file (http(s) targets have nothing to stat).
func localPath(u string) string {
	switch {
	case strings.HasPrefix(u, "file://"):
		return strings.TrimPrefix(u, "file://")
	case strings.HasPrefix(u, "/"):
		return u
	}
	return ""
}

// openQueueStore opens the SAME index the daemon queues into.
//
// `retry` mutates the jobs table, so it opens a store directly rather than
// calling the daemon. But "directly" used to mean `addStoreFlags`, which
// resolves the project-LOCAL home — and when a daemon is running, ingest and
// status use its project-NAMESPACED index instead. The two diverged silently:
// one index had 131 failed jobs and the other, which retry was requeuing, had
// 21 and nothing writing to it.
//
// So: if a daemon owns this project, open its scoped index by path. Otherwise
// fall back to the local home, which is what --embedded and --db already mean.
func openQueueStore(openStore func() (*raglit.Store, error), homeOf func() raglit.Home,
	client func(func() raglit.Home, bool) (string, string, error),
	dbSet bool, indexFlag string) (*raglit.Store, string, error) {

	durl, ns, err := client(homeOf, dbSet)
	// No daemon, no project, or an explicit --db/--embedded: the local home is
	// the right answer and a resolution error here is not fatal.
	if err != nil || durl == "" || ns == "" {
		st, oerr := openStore()
		if oerr != nil {
			return nil, "", oerr
		}
		return st, string(homeOf()) + " (local)", nil
	}
	root := raglit.DefaultRoot()
	if st, ok := readDaemonState(root); ok && st.Root != "" {
		root = st.Root
	}
	name := ns + nsSep + raglit.NormalizeIndexName(resolveIndexName(indexFlag, homeOf))
	home := raglit.Home(filepath.Join(root, "indexes", name))
	// A namespaced index that does not exist yet would be CREATED by OpenHome,
	// leaving an empty index and a retry that requeues nothing while reporting
	// success. Say so instead.
	if _, serr := os.Stat(home.IndexPath()); serr != nil {
		return nil, "", fmt.Errorf("no index %q under %s — is the daemon holding this project's queue? (%v)", name, root, serr)
	}
	st, oerr := raglit.OpenHome(home)
	if oerr != nil {
		return nil, "", oerr
	}
	return st, string(home) + " (daemon-scoped)", nil
}
