// Command raglit is a document index and search service for LLM sessions:
// BM25 and vector search over SQLite, at document:page:fragment grain. See
// `raglit help`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
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

// addHomeFlags registers --home and --index for a command that names its
// indexes itself (a read selector, an ingest target): homeOf finds the
// project config, index is the raw --index value.
func addHomeFlags(fs *flag.FlagSet) (homeOf func() raglit.Home, index *string) {
	home := fs.String("home", "", "project home dir (default: nearest ./.raglit, else $RAGLIT_HOME)")
	index = fs.String("index", "", "index name (default: config default_index, else 'default')")
	homeOf = func() raglit.Home {
		if *home != "" {
			return raglit.Home(*home)
		}
		return raglit.DiscoverHome()
	}
	return homeOf, index
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

// runIndex queues local files and directories (text and PDF) on the daemon
// and waits for it to work them. It used to index them in-process; the daemon
// is the only worker now (USER 2026-10-30: always a daemon).
func runIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	resolve := addTargetFlags(fs)
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("index: no files/dirs given")
	}
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
	for i, p := range files {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		files[i] = abs
	}
	t, err := resolve()
	if err != nil {
		return err
	}
	if err := daemonIngest(t.base, files, t.index, "", false, projectDirOf(t.home)); err != nil {
		return err
	}
	return waitForQueue(t.base, t.index)
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	homeOf, index := addHomeFlags(fs)
	client := addClientFlags(fs) // --daemon + --project
	shared := addSharedFlag(fs)
	limit := fs.IntP("limit", "n", 10, "max results")
	mode := fs.String("mode", "", "bm25 | vec | hybrid (default: hybrid where the index has embeddings, else bm25)")
	path := fs.String("path", "", "constrain to documents whose path starts with this prefix (a subtree)")
	fs.Parse(args)
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("search: empty query")
	}
	dURL, ns, err := client(homeOf)
	if err != nil && dURL == "" {
		return err
	}
	// Empty --index → the indexes scoped to this directory and the ambient
	// globals; with an explicit --project, that project's and its shared.
	cv, err := readView(fs, dURL, ns, err, homeOf, projectShared(homeOf, shared()...))
	if err != nil {
		return err
	}
	return daemonSearchPrint(dURL, query, cv.read(*index), *mode, *path, *limit, ns)
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
	for n > 0 && !utf8.RuneStart(s[n]) {
		n-- // never cut inside a character
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
	fs := flag.NewFlagSet("reread", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	dry := fs.Bool("dry-run", false, "name what would be re-read")
	suspectRoot := fs.String("suspect", "", "take the targets from the transcription plausibility check under DIR")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()
	if *suspectRoot != "" {
		found, err := raglit.SuspectDocs(*suspectRoot)
		if err != nil {
			return err
		}
		for doc, pages := range found {
			fmt.Printf("  page(s) %v look wrong: %s\n", pages, doc)
			targets = append(targets, doc)
		}
		if len(targets) == 0 {
			fmt.Println("no suspect transcriptions under", *suspectRoot)
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
	if *dry {
		fmt.Printf("would re-read %d document(s)\n", len(targets))
		return nil
	}
	t, err := resolve()
	if err != nil {
		return err
	}
	// Rulings, for the copy announcement below. A project without any is the
	// normal state and must not stop a reread, so a failure here is not fatal.
	js, _ := openJudgements()
	if js != nil {
		defer js.Close()
	}
	// Both halves — purging the cached pages and re-reading — are the daemon's.
	for _, p := range targets {
		n, id, err := daemonReread(t, p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			continue
		}
		fmt.Printf("  purged %d cached page(s), queued job %d: %s\n", n, id, p)
		announceOtherCopies(t, js, p)
	}
	return nil
}
