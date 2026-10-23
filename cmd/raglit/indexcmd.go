package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// `raglit index` (USER 2026-10-23): one place for per-index work.
//
//	raglit index --list               every index: scope, and whether it applies here
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
		"scope":   {"[add|rm PATTERN...] — directories a search with no index named reads it from", indexScope},
		"global":  {"[ambient|opt-in] — no scopes: every search reads it (ambient) or only when named", indexGlobal},
	}
}

func runIndexCmd(args []string) error {
	if len(args) > 0 && (args[0] == "--list" || args[0] == "list") {
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
	if err != nil && !(errors.Is(err, errNoProject) && base != "") {
		return
	}
	if base == "" {
		err = fmt.Errorf("index %s: needs the daemon — remove --embedded/--db", name)
		return
	}
	full, err = indexFullName(base, ns, projectShared(homeOf), name)
	return
}

// indexFullName is the daemon's name for NAME: "proj:idx" is that project's
// index; a bare name is this project's, or, with no project, the index of that
// full name or the one index whose local name it is.
func indexFullName(base, ns string, shared []string, name string) (string, error) {
	if d, ok := nsNamedIndex(name); ok {
		return d, nil
	}
	if ns != "" {
		return nsWriteIndex(ns, shared, name)
	}
	b, err := daemonGet(base, "/indexes", nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Indexes []idxRow `json:"indexes"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	names := make([]string, 0, len(out.Indexes))
	for _, r := range out.Indexes {
		names = append(names, r.Name)
	}
	if full := uniqueLocal(names, name); containsStr(names, full) {
		return full, nil
	}
	return "", fmt.Errorf("no index %q (or more than one project has one: name it proj:%s)", name, name)
}

func indexScope(name string, args []string) error {
	base, full, rest, err := indexTarget(name, args, nil)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if len(rest) > 0 {
		verb, pats := rest[0], rest[1:]
		if len(pats) == 0 || (verb != "add" && verb != "rm") {
			return fmt.Errorf("usage: raglit index %s scope [add|rm PATTERN...]", name)
		}
		for i, p := range pats {
			if pats[i], err = scopePattern(p); err != nil {
				return err
			}
		}
		if verb == "add" {
			body["add"] = pats
		} else {
			body["remove"] = pats
		}
	}
	return printScope(base, full, body)
}

func indexGlobal(name string, args []string) error {
	base, full, rest, err := indexTarget(name, args, nil)
	if err != nil {
		return err
	}
	mode := "opt-in"
	if len(rest) > 0 {
		mode = rest[0]
	}
	if len(rest) > 1 || (mode != "ambient" && mode != "opt-in") {
		return fmt.Errorf("usage: raglit index %s global [ambient|opt-in]", name)
	}
	return printScope(base, full, map[string]any{"scopes": []string{}, "ambient": mode == "ambient"})
}

func printScope(base, full string, body map[string]any) error {
	b, err := daemonPostJSON(base, "/api/index/scope?"+urlValues("index", full).Encode(), body)
	if err != nil {
		return err
	}
	var out struct {
		Scopes  []string `json:"scopes"`
		Ambient bool     `json:"ambient"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", full, scopeText(out.Scopes, out.Ambient))
	return nil
}

func scopeText(scopes []string, ambient bool) string {
	switch {
	case len(scopes) > 0:
		return strings.Join(scopes, ", ")
	case ambient:
		return "global, ambient (every search)"
	default:
		return "global, opt-in (only when named)"
	}
}

// scopePattern makes a scope absolute: "." is this directory, "~" the home
// directory, a relative path is joined to this directory. A glob keeps its
// wildcards.
func scopePattern(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = filepath.Join(wd, p)
	}
	return p, nil
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

// runIndexList prints every index the daemon holds: documents, state, its
// scopes (or global mode), and ✓ when a search from here with no index named
// reads it. Configured roots are joined for this project's indexes.
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
	if err != nil && !(errors.Is(err, errNoProject) && base != "") {
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
	wd, _ := os.Getwd()
	cfgHome := homeOf()
	if h, err := watchHome(*homeFlag, fs.Lookup("project").Value.String()); err == nil {
		cfgHome = h
	}
	cfg, _, _ := raglit.LoadConfig(cfgHome)
	baseDir := filepath.Dir(string(cfgHome))
	seen := map[string]bool{}
	w := 8
	for _, r := range out.Indexes {
		w = max(w, len(r.Name))
	}
	for _, r := range out.Indexes {
		if r.Documents == 0 && r.Name == "default" {
			continue // always listed by the daemon; empty unless something used it
		}
		here := " "
		if len(r.Scopes) > 0 && raglit.ScopeMatches(r.Scopes, wd) || len(r.Scopes) == 0 && r.Ambient {
			here = "✓"
		}
		state := ""
		if r.Paused {
			state = "  paused"
		}
		var fed string
		if local, own := strings.CutPrefix(r.Name, ns+nsSep); ns != "" && own {
			seen[local] = true
			fed = roots(cfg, local, baseDir)
		}
		fmt.Printf("%s %-*s %7d docs  %s%s%s\n", here, w, r.Name, r.Documents, scopeText(r.Scopes, r.Ambient), state, fed)
	}
	// Configured but never ingested: no documents yet, so the daemon has no row.
	for name := range cfg.Indexes {
		if !seen[name] && ns != "" {
			fmt.Printf("  %-*s %7d docs  empty%s\n", w, ns+nsSep+name, 0, roots(cfg, name, baseDir))
		}
	}
	fmt.Println("\n✓ = a search from here with no --index reads it.  raglit index NAME scope|global")
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
