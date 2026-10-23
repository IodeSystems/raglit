package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// `raglit index` (USER 2026-10-23): one place for per-index work.
//
//	raglit index --list               this project's indexes
//	raglit index NAME SUBCOMMAND ...  one index (indexSubs)
//	raglit index FILE|DIR...          legacy: index files in-process now

type indexSub struct {
	usage string
	run   func(name string, args []string) error
}

var indexSubs map[string]indexSub

func init() {
	withIndex := func(run func([]string) error) func(string, []string) error {
		return func(name string, args []string) error {
			return run(append([]string{"--index", name}, args...))
		}
	}
	indexSubs = map[string]indexSub{
		"status":  {"queue and document counts", withIndex(runStatus)},
		"search":  {`"query" — search this index`, withIndex(runSearch)},
		"forget":  {"PATH... — drop documents from the index; files stay", withIndex(runForget)},
		"sync":    {"queue this index's configured roots", withIndex(runSync)},
		"pause":   {"stop this index's work; jobs keep queueing", indexPause("pause")},
		"resume":  {"resume a paused index", indexPause("resume")},
		"purge":   {"[--yes] GLOB — remove documents matching GLOB", indexPurge},
		"unwatch": {"DIR — stop indexing DIR into this index; its documents stay", indexUnwatch},
	}
}

func runIndexCmd(args []string) error {
	if len(args) > 0 && (args[0] == "--list" || args[0] == "-list" || args[0] == "list") {
		return runIndexList(args[1:])
	}
	if len(args) >= 1 && !strings.HasPrefix(args[0], "-") {
		if len(args) >= 2 {
			if s, ok := indexSubs[args[1]]; ok {
				return s.run(args[0], args[2:])
			}
		}
		if _, err := os.Stat(args[0]); err != nil {
			// Not a file to index, so NAME with a missing or unknown subcommand.
			if len(args) >= 2 && args[1] != "-h" && args[1] != "--help" && args[1] != "help" {
				fmt.Fprintf(os.Stderr, "raglit index %s: unknown subcommand %q\n\n", args[0], args[1])
			}
			printIndexSubs(args[0])
			return nil
		}
	}
	return runIndex(args)
}

func printIndexSubs(name string) {
	names := make([]string, 0, len(indexSubs))
	for n := range indexSubs {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stderr, "raglit index %s SUBCOMMAND:\n", name)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-8s %s\n", n, indexSubs[n].usage)
	}
}

// indexTarget resolves NAME to the daemon's namespaced index, from the
// --project/--daemon/--home flags in args, and returns the remaining
// arguments.
func indexTarget(name string, args []string, extra func(*flag.FlagSet)) (base, full string, rest []string, err error) {
	fs := flag.NewFlagSet("index "+name, flag.ContinueOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	client := addClientFlags(fs)
	if extra != nil {
		extra(fs)
	}
	if rest, err = parseInterleaved(fs, args); err != nil {
		return
	}
	homeOf := func() raglit.Home {
		if *homeFlag != "" {
			return raglit.Home(*homeFlag)
		}
		return raglit.DiscoverHome()
	}
	base, ns, err := client(homeOf, false)
	if err != nil {
		return
	}
	if base == "" {
		err = fmt.Errorf("index %s: needs the daemon — remove --embedded/--db", name)
		return
	}
	full, err = nsWriteIndex(ns, projectShared(homeOf), name)
	return
}

func indexPause(verb string) func(string, []string) error {
	return func(name string, args []string) error {
		base, full, _, err := indexTarget(name, args, nil)
		if err != nil {
			return err
		}
		return runPause(verb, []string{"--daemon", base, full})
	}
}

func indexPurge(name string, args []string) error {
	var yes *bool
	base, full, rest, err := indexTarget(name, args, func(fs *flag.FlagSet) {
		yes = fs.Bool("yes", false, "remove the documents; without it, only report what would go")
	})
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: raglit index %s purge [--yes] GLOB", name)
	}
	a := []string{"--daemon", base}
	if *yes {
		a = append(a, "--yes")
	}
	return runPurge(append(a, full, rest[0]))
}

func indexUnwatch(name string, args []string) error {
	fs := flag.NewFlagSet("index "+name+" unwatch", flag.ContinueOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	var project string
	fs.StringVar(&project, "project", "", "project name, when there is no ./.raglit")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: raglit index %s unwatch DIR", name)
	}
	home, err := watchHome(*homeFlag, project)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	name = raglit.NormalizeIndexName(name)
	removed, err := removeWatchRoot(home, name, dir)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%s is not a root of index %q in %s", dir, name, home.ConfigPath())
	}
	fmt.Printf("stopped indexing %s into %q; its documents stay (remove them: raglit index %s purge '%s/**')\n",
		dir, name, name, dir)
	return nil
}

// runIndexList prints this project's indexes: the daemon's counts, joined
// with the roots the config feeds each one.
func runIndexList(args []string) error {
	fs := flag.NewFlagSet("index --list", flag.ContinueOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	client := addClientFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	homeOf := func() raglit.Home {
		if *homeFlag != "" {
			return raglit.Home(*homeFlag)
		}
		return raglit.DiscoverHome()
	}
	base, ns, err := client(homeOf, false)
	if err != nil {
		return err
	}
	if base == "" {
		return fmt.Errorf("index --list: needs the daemon — remove --embedded/--db")
	}
	b, err := daemonGet(base, "/indexes", nil)
	if err != nil {
		return err
	}
	var out struct {
		Indexes []idxRow `json:"indexes"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	cfgHome := homeOf()
	if h, err := watchHome(*homeFlag, fs.Lookup("project").Value.String()); err == nil {
		cfgHome = h
	}
	cfg, _, _ := raglit.LoadConfig(cfgHome)
	baseDir := filepath.Dir(string(cfgHome))
	seen := map[string]bool{}
	for _, r := range out.Indexes {
		if ns != "" && !strings.HasPrefix(r.Name, ns+nsSep) {
			continue
		}
		local := nsStrip(ns, r.Name)
		seen[local] = true
		state := "active"
		if r.Paused {
			state = "paused"
		}
		fmt.Printf("%-24s %7d docs  %s%s\n", local, r.Documents, state, roots(cfg, local, baseDir))
	}
	// Configured but never ingested: no documents yet, so the daemon has no row.
	for name := range cfg.Indexes {
		if !seen[name] {
			fmt.Printf("%-24s %7d docs  %s%s\n", name, 0, "empty", roots(cfg, name, baseDir))
		}
	}
	return nil
}

func roots(cfg raglit.Config, name, baseDir string) string {
	var rs []string
	for _, r := range cfg.Indexes[name].Roots {
		rs = append(rs, rootDir(baseDir, r.Path))
	}
	if len(rs) == 0 {
		return ""
	}
	return "  " + strings.Join(rs, ", ")
}
