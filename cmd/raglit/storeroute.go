package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/iodesystems/raglit"
)

// Which index a command actually opens.
//
// raglit has two places an index can live and they are routinely different
// databases. A project-local .raglit/index.sqlite is what `--embedded` and a
// pre-daemon setup use. A daemon-routed project ingests through the shared
// daemon, whose indexes live scoped under <root>/indexes/<project>__<name>.
//
// `openStore` resolves the FIRST of those, always. So on ardley-v-brannock —
// daemon-routed, 397 documents in the daemon's index — `similar`, `marks` and
// `reread` were opening a stale local file holding 257, and answering
// confidently about a corpus nobody is using. Nothing errored: both files are
// real indexes, one is simply not the one in service.
//
// So a command that reports on the corpus asks for the corpus, not for whatever
// index happens to be underfoot.
//
// Read-only, deliberately. The daemon is the single writer for its indexes, and
// that is what stops a CLI fighting the worker pool over one file. SQLite in WAL
// mode takes any number of concurrent readers alongside that writer, so
// reporting is safe where writing is not — and a command that needs to WRITE
// must go through the daemon rather than opening the file behind its back.

// openCorpus opens the index this project is actually using, for reading.
//
// Falls back to the local index when the project is not daemon-routed, which is
// the embedded case and correct there. `--db` and `--home` still win, because a
// caller naming a file means that file.
func openCorpus(fs *flag.FlagSet, localOpen func() (*raglit.Store, error), homeOf func() raglit.Home) (*raglit.Store, error) {
	if explicitStoreFlag(fs) {
		return localOpen()
	}
	ns, err := resolveProject("", homeOf)
	if err != nil || ns == "" {
		// Not daemon-routed (no project namespace): the local index IS the corpus.
		return localOpen()
	}
	name := nsIndex(ns, resolveIndexName("", homeOf))
	home := raglit.ScopedIndexHome(raglit.DefaultRoot(), name)
	st, err := raglit.OpenIndexRO(home, "default")
	if err != nil {
		// The daemon's index is not there. Say which one is being read instead,
		// because the difference decides whether a report means anything.
		fmt.Fprintf(os.Stderr,
			"warning: no daemon index for %q — reading the project-local index, which may be behind\n", name)
		return localOpen()
	}
	return st, nil
}

// explicitStoreFlag reports whether the caller named a specific index or home.
func explicitStoreFlag(fs *flag.FlagSet) bool {
	named := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "db", "home", "index":
			named = true
		}
	})
	return named
}
