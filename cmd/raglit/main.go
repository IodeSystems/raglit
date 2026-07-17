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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "index":
		err = runIndex(os.Args[2:])
	case "search":
		err = runSearch(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
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
  raglit index  --db idx.sqlite FILE|DIR...   ingest text/markdown files
  raglit search --db idx.sqlite [-n N] "query"
  raglit serve  --db idx.sqlite [-n N]         stdio MCP server (search tool)

flags:
  --home  index home dir (default $RAGLIT_HOME or ~/local/raglit); holds
          index.sqlite + originals/ + pages/
  --db    raw index file path (overrides --home; skips originals storage)
  -n      search/serve: max (default) results
`)
}

// addStoreFlags registers --home/--db on fs and returns an opener to call after
// fs.Parse. --db (raw path) wins if set; otherwise --home (or the default home)
// is used, which also stores ingested originals.
func addStoreFlags(fs *flag.FlagSet) func() (*raglit.Store, error) {
	home := fs.String("home", "", "index home dir (default $RAGLIT_HOME or ~/local/raglit)")
	db := fs.String("db", "", "raw index file path (overrides --home)")
	return func() (*raglit.Store, error) {
		if *db != "" {
			return raglit.Open(*db)
		}
		h := raglit.DefaultHome()
		if *home != "" {
			h = raglit.Home(*home)
		}
		return raglit.OpenHome(h)
	}
}

func runIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	openStore := addStoreFlags(fs)
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("index: no files/dirs given")
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

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
				if !d.IsDir() && isText(p) {
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

	n := 0
	for _, p := range files {
		doc, err := readDoc(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", p, err)
			continue
		}
		if err := store.Ingest(doc); err != nil {
			return err
		}
		fmt.Printf("indexed %s (%d fragments)\n", p, len(doc.Fragments))
		n++
	}
	fmt.Printf("done: %d document(s) → %s\n", n, store.Path())
	return nil
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	openStore := addStoreFlags(fs)
	limit := fs.Int("n", 10, "max results")
	fs.Parse(args)
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("search: empty query")
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	hits, err := store.Search(query, *limit)
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

// readDoc reads a text/markdown file and splits it into fragments on blank
// lines (paragraph grain). Pageless (page 0) — PDFs will carry real pages.
func readDoc(path string) (raglit.Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return raglit.Document{}, err
	}
	doc := raglit.Document{Path: path, Title: filepath.Base(path)}
	ord := 0
	for _, block := range strings.Split(string(b), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		doc.Fragments = append(doc.Fragments, raglit.Fragment{Ord: ord, Text: strings.TrimSpace(block)})
		ord++
	}
	return doc, nil
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
