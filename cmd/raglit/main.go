// Command raglit is a local document RAG index: BM25 search over a single
// portable SQLite file, at document:page:fragment grain.
//
//	raglit index --db idx.sqlite FILE|DIR...   ingest text/markdown into the index
//	raglit search --db idx.sqlite "query"      BM25-ranked fragments, best first
//
// PDF pagify + vision-LLM OCR (feeding the same index) and an MCP `serve` mode
// land next; this is the offline lexical core.
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

func main() {
	if len(os.Args) < 2 {
		// No command: run the setup wizard if this home isn't initialized yet,
		// otherwise show usage. (raglit is unusable until `init` writes config.)
		if !raglit.Inited(raglit.DefaultHome()) {
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
	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(os.Args[2:])
	case "index":
		err = runIndex(os.Args[2:])
	case "ingest":
		err = runIngest(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "work":
		err = runWork(os.Args[2:])
	case "search":
		err = runSearch(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "daemon":
		err = runDaemon(os.Args[2:])
	case "demo":
		err = runDemo(os.Args[2:])
	case "pagify":
		err = runPagify(os.Args[2:])
	case "ocr":
		err = runOcr(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "raglit: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `raglit — local BM25 document index (SQLite FTS5)

usage:
  raglit init   [--home DIR]                 configure endpoint + models (wizard)
  raglit demo                                self-contained offline tour
  raglit index  [--home DIR] [--embed] FILE|DIR...   ingest local files (+ PDFs via OCR)
  raglit ingest [--home DIR] [--now] TARGET...  queue folders/files/URLs (file://, http(s)://)
  raglit work   [--home DIR] [--embed]       drain the ingest queue once, then exit
  raglit status [--home DIR]                 index + queue status (done/pending/rate/eta)
  raglit search [--home DIR] [--mode M] [-n N] "query"
  raglit serve  [--home DIR] [-n N] [--embed]   stdio MCP server (search + ingest + index_status)
  raglit daemon [--home DIR] [--addr :7420] [--embed]   HTTP daemon (ingest/search/status)
  # add --daemon URL (or $RAGLIT_DAEMON) to ingest/search/status to call a daemon
  raglit pagify [--out DIR] FILE.pdf...      extract page images (image/scanned PDFs)
  raglit ocr    [--llm-*] IMAGE...           transcribe page images via a vision model

flags:
  --home        index home dir (default $RAGLIT_HOME or ~/local/raglit); holds
                index.sqlite + originals/ + pages/
  --db          raw index file path (overrides --home; skips originals storage)
  -n            search/serve: max (default) results
  --embed       index: also embed fragments for vector/hybrid search
  --mode        search: bm25 (default) | vec | hybrid  (vec/hybrid need --embed'd index)
  --llm-url     model base URL (default https://llm.iodesystems.com)
  --llm-model   vision model id (default ternary-bonsai-27b)
  --embed-model embedding model id (default nomic-embed-text)
  --context-tokens  known model context window (e.g. 256000); 0 = config/probe
  --llm-key     API key (or $RAGLIT_LLM_KEY)

PDF indexing extracts embedded page images (pure-Go; image/scanned PDFs only)
and OCRs each page via the vision model. --embed adds nomic vectors; search
--mode hybrid fuses BM25 + cosine with reciprocal-rank fusion.
`)
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
		return raglit.DefaultHome()
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
				if !d.IsDir() && (isText(p) || isPDF(p)) {
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
		if _, err := store.Enqueue(abs, ""); err != nil {
			return err
		}
	}
	n, err := buildWorker(store, lf, homeOf()).Drain(context.Background())
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
	daemon := addDaemonFlag(fs)
	limit := fs.Int("n", 10, "max results")
	mode := fs.String("mode", "bm25", "bm25 | vec | hybrid (vec/hybrid need embeddings)")
	fs.Parse(args)
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("search: empty query")
	}

	// Client mode: query a running daemon.
	if *daemon != "" {
		return daemonSearchPrint(*daemon, query, resolveIndexName(fs.Lookup("index").Value.String(), homeOf), *mode, *limit)
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	var hits []raglit.Hit
	switch *mode {
	case "bm25":
		hits, err = store.Search(query, *limit)
	case "vec", "hybrid":
		lf.resolve(homeOf())
		if err := lf.requireEmbed(); err != nil {
			return err
		}
		store.SetEmbedder(lf.embedder())
		if *mode == "vec" {
			hits, err = store.VecSearch(ctx, query, *limit)
		} else {
			hits, err = store.HybridSearch(ctx, query, *limit)
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
		fmt.Printf("%d. [%.3f] %s\n   %s\n", i+1, h.Score, loc, clip(oneLine(h.Text), 160))
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
