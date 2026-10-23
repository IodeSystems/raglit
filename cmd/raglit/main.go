// Command raglit is a document index and search service for LLM sessions:
// BM25 and vector search over SQLite, at document:page:fragment grain. See
// `raglit help`.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

func main() {
	// Before anything else: a source-stamped build rebuilds itself when the tree
	// changed and re-execs. No-op for a released binary (srcDir empty) and for
	// spawned children. See selfbuild.go.
	selfUpdate()
	installDaemonAuth()
	if len(os.Args) < 2 {
		// No command: run the setup wizard if this home isn't initialized yet,
		// otherwise show usage. (raglit is unusable until `init` writes config.)
		if !raglit.Inited(raglit.DiscoverHome()) {
			fmt.Fprintln(os.Stderr, "raglit is not configured yet — starting setup.")
			if err := runInit(nil); err != nil {
				fmt.Fprintf(os.Stderr, "raglit: %v\n", err)
				os.Exit(1)
			}
			return
		}
		usage()
		os.Exit(2)
	}
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "raglit: %v\n", err)
		os.Exit(1)
	}
}

// addStoreFlags registers --home/--db on fs and returns an opener to call after
// fs.Parse. --db (raw path) wins if set; otherwise --home (or the default home)
// is used, which also stores ingested originals.
func addStoreFlags(fs *flag.FlagSet) (open func() (*raglit.Store, error), homeOf func() raglit.Home) {
	home := fs.String("home", "", "index home dir (default $RAGLIT_HOME or ~/local/raglit)")
	db := fs.String("db", "", "raw index file path (overrides --home)")
	index := fs.String("index", "", "index name (default: config default_index, else 'default')")
	homeOf = func() raglit.Home {
		if *home != "" {
			return raglit.Home(*home)
		}
		return raglit.DiscoverHome()
	}
	open = func() (*raglit.Store, error) {
		if *db != "" {
			return raglit.Open(*db)
		}
		return raglit.OpenIndex(homeOf(), resolveIndexName(*index, homeOf))
	}
	return open, homeOf
}

// resolveIndexName picks the effective index: an explicit --index wins, else the
// home's config default_index, else "default".
func resolveIndexName(flagVal string, homeOf func() raglit.Home) string {
	if flagVal != "" {
		return flagVal
	}
	if cfg, _, _ := raglit.LoadConfig(homeOf()); cfg.DefaultIndex != "" {
		return cfg.DefaultIndex
	}
	return "default"
}

// resolveDaemon picks the effective daemon URL to route to: an explicit --daemon
// (or $RAGLIT_DAEMON, its flag default) wins, else the home config's daemon_url.
// Empty → local/embedded mode (open the index directly). This is what makes a
// project's .raglit/ a CLIENT config: set daemon_url and every command talks to
// the daemon without passing --daemon each time.
func resolveDaemon(flagVal string, homeOf func() raglit.Home) string {
	if flagVal != "" {
		trustDaemon(flagVal)
		return flagVal
	}
	if cfg, _, _ := raglit.LoadConfig(homeOf()); cfg.DaemonURL != "" {
		trustDaemon(cfg.DaemonURL)
		return cfg.DaemonURL
	}
	return ""
}

func runIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs) // vision model for PDFs; embed model when --embed
	embed := fs.Bool("embed", false, "also embed fragments for vector/hybrid search")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("index: no files/dirs given")
	}
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
		if ie := buildImageEmbedder(homeOf()); ie != nil {
			store.SetImageEmbedder(ie)
		}
	}

	// Local index goes through the SAME pipeline as URL ingest: enqueue each
	// file, then drain now. That gives local files the LLM segmentation (text +
	// code) and PDF OCR + concurrent embed — one code path, no duplicate splitter.
	var files []string
	for _, root := range fs.Args() {
		fi, err := os.Stat(root)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && !raglit.IsGeneratedSidecar(p) && (isText(p) || isPDF(p)) {
					files = append(files, p)
				}
				return nil
			})
			if err != nil {
				return err
			}
		} else {
			files = append(files, root)
		}
	}
	for _, p := range files {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if _, err := store.Enqueue(abs, ""); err != nil && !errors.Is(err, raglit.ErrIgnored) {
			return err
		}
	}
	n, err := buildWorker(store, lf, homeOf(), nil).Drain(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("indexed %d file(s) → %s\n", n, store.Path())
	return nil
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	client := addClientFlags(fs) // --daemon + --embedded
	shared := addSharedFlag(fs)
	limit := fs.IntP("limit", "n", 10, "max results")
	mode := fs.String("mode", "", "bm25 | vec | hybrid (default: hybrid where the index has embeddings, else bm25)")
	path := fs.String("path", "", "constrain to documents whose path starts with this prefix (a subtree)")
	fs.Parse(args)
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("search: empty query")
	}

	// Default: query the shared daemon (auto-started if needed); --embedded/--db
	// open the index in-process.
	dURL, ns, err := client(homeOf, fs.Lookup("db").Value.String() != "")
	if err != nil && dURL == "" {
		return err
	}
	if dURL != "" {
		// Empty --index → the indexes scoped to this directory and the ambient
		// globals; with an explicit --project, that project's and its shared.
		cv, err := readView(fs, dURL, ns, err, homeOf, projectShared(homeOf, shared()...))
		if err != nil {
			return err
		}
		return daemonSearchPrint(dURL, query, cv.read(fs.Lookup("index").Value.String()), *mode, *path, *limit, ns)
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	var hits []raglit.Hit
	if *mode == "" {
		*mode = "bm25"
		if lf.resolve(homeOf()); lf.requireEmbed() == nil {
			*mode = "hybrid"
		}
	}
	switch *mode {
	case "bm25":
		hits, err = store.SearchPath(query, *path, *limit)
	case "vec", "hybrid":
		lf.resolve(homeOf())
		if err := lf.requireEmbed(); err != nil {
			return err
		}
		store.SetEmbedder(lf.embedder())
		if *mode == "vec" {
			hits, err = store.VecSearchPath(ctx, query, *path, *limit)
		} else {
			hits, err = store.HybridSearchPath(ctx, query, *path, *limit)
		}
	default:
		return fmt.Errorf("search: unknown --mode %q (bm25|vec|hybrid)", *mode)
	}
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Println("(no matches)")
		return nil
	}
	for i, h := range hits {
		loc := h.Path
		if h.Page > 0 {
			loc = fmt.Sprintf("%s p%d", h.Path, h.Page)
		}
		// The hit is on GENERATED text — a caption of the document, or its type's
		// schema filled out from it. Said here rather than left to the page
		// number, because there is no page and the text reads like the document.
		switch h.Origin {
		case "identity":
			loc += "  [what this document is — a description of it, not its words]"
		case "fields":
			loc += "  [fields READ OUT of this document — cite the document, not this]"
		default:
			if h.IsDescription() {
				loc += "  [generated text, not the document's own words]"
			}
		}
		fmt.Printf("%d. [%.3f] %s%s\n   %s\n", i+1, h.Score, loc, hitMeasures(h.Similarity, h.Matched, h.QueryTerms), clip(oneLine(h.Text), 160))
	}
	return nil
}

func isText(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".txt", ".md", ".markdown", ".rst", ".text":
		return true
	}
	return false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// runReread purges the cached page OCR for a document and reads it again.
//
// `raglit index` on a document whose OCR was wrong is a NO-OP: the page cache is
// keyed by the image's SHA, so the same pixels get the same answer back. That is
// right when the answer was right, and it is why re-ingesting a 200-page scan is
// free — but it means a bad read is permanent until somebody purges it, and
// nothing offered a way to. Observed: five documents re-indexed to fix
// watermark-only reads, four "completed" in twenty seconds, not one byte changed.
//
// Purge, then hand off to runIndex. Deliberately NOT a second ingest path — the
// point is to make the normal path do the work again, and a parallel pipeline
// here would be one more thing to keep in step with the real one.
//
// --suspect takes its targets from the transcription plausibility check instead
// of making somebody name them, which is the pairing that makes the check worth
// having.
func runReread(args []string) error {
	// Split our own flags out and pass everything else through to index, so
	// --home, --index, --model and friends keep working without being restated.
	var passthrough, targets []string
	suspectRoot, dry := "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "--suspect="):
			suspectRoot = a[strings.Index(a, "=")+1:]
		case a == "--suspect":
			if i+1 < len(args) {
				i++
				suspectRoot = args[i]
			}
		case strings.HasPrefix(a, "-"):
			passthrough = append(passthrough, a)
			// A flag that takes a value: keep the value with it.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && strings.Contains(a, "=") == false {
				i++
				passthrough = append(passthrough, args[i])
			}
		default:
			targets = append(targets, a)
		}
	}

	if suspectRoot != "" {
		found, err := raglit.SuspectDocs(suspectRoot)
		if err != nil {
			return err
		}
		for doc, pages := range found {
			fmt.Printf("  page(s) %v look wrong: %s\n", pages, doc)
			targets = append(targets, doc)
		}
		if len(targets) == 0 {
			fmt.Println("no suspect transcriptions under", suspectRoot)
			return nil
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("reread: name a document, or pass --suspect DIR")
	}
	// Absolute, before anything is done with a target. A document is identified in
	// the index by its path, and a relative one names a different file for every
	// working directory — including the daemon's, which is not this process's. Sent
	// as-is it inserts a SECOND row for a file already indexed, and that row can
	// never be opened again: the daemon has no way to learn the cwd it was typed
	// in. --suspect makes this the common case, since it hands back whatever shape
	// the root it walked was written as.
	for i, t := range targets {
		abs, err := filepath.Abs(t)
		if err != nil {
			return fmt.Errorf("reread %s: %w", t, err)
		}
		targets[i] = abs
	}
	if dry {
		fmt.Printf("would re-read %d document(s)\n", len(targets))
		return nil
	}

	purge := flag.NewFlagSet("reread-purge", flag.ContinueOnError)
	purge.SetOutput(io.Discard)
	openStore, homeOf := addStoreFlags(purge)
	_ = purge.Parse(passthrough)

	// Rulings, for the copy announcement below. A project without any is the
	// normal state and must not stop a reread, so a failure here is not fatal.
	js, _ := openJudgements()
	if js != nil {
		defer js.Close()
	}

	// A reread WRITES: it purges cached pages and re-reads. On a daemon-routed
	// project both halves belong to the daemon — doing them locally would purge a
	// different index from the one in service and leave the bad read in place.
	if ns, err := resolveProject("", homeOf); err == nil && ns != "" && !explicitStoreFlag(purge) {
		read, rerr := openCorpus(purge, openStore, homeOf)
		if rerr != nil {
			return rerr
		}
		defer read.Close()
		for _, t := range targets {
			n, id, err := daemonReread(t)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
				continue
			}
			fmt.Printf("  purged %d cached page(s), queued job %d: %s\n", n, id, t)
			announceOtherCopies(read, js, t)
		}
		return nil
	}

	// Embedded/local: purge with a store opened only for that, then close it
	// before index opens its own — two writers on one sqlite file is a lock
	// fight for no gain.
	store, err := openStore()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, t := range targets {
		n, err := store.PurgeDocPageCache(ctx, t)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			continue
		}
		fmt.Printf("  purged %d cached page(s): %s\n", n, t)
		announceOtherCopies(store, js, t)
	}
	store.Close()

	return runIndex(append(passthrough, targets...))
}
