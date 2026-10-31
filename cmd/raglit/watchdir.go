package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// `raglit watch DIR --index NAME` (USER 2026-10-23): index a directory once and
// let many sessions use it. Before, that took hand-editing the config's
// `indexes`, then `sync`, then `watch start`. This is those three steps:
//
//  1. add DIR to indexes.NAME.roots in this project's config (once),
//  2. set "watch": true, so the daemon re-ingests on change and `sync`
//     re-registers after a restart,
//  3. sync NAME, which queues the files and registers the watch.
//
// The index is created by its first ingest. Running it again changes nothing in
// the config and queues nothing the daemon has not seen change.

// isDirArg reports whether a watch argument names a directory rather than a
// subcommand. A directory called "list" in the working directory loses to the
// subcommand; ./list reaches it.
func isDirArg(a string) bool {
	switch a {
	case "start", "add", "list", "ls", "stop", "rm", "remove":
		return false
	}
	fi, err := os.Stat(a)
	return err == nil && fi.IsDir()
}

func runWatchDir(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	addClientFlags(fs) // --daemon + --project, forwarded to sync
	index := fs.String("index", "", "index to watch DIR into (required)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("watch: usage: raglit watch DIR --index NAME")
	}
	if strings.TrimSpace(*index) == "" {
		return fmt.Errorf("watch: --index NAME is required")
	}
	if strings.Contains(*index, ":") {
		return fmt.Errorf("watch: %q names another project's index; watch into one of this project's", *index)
	}
	dir, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("watch: %s is not a directory", pos[0])
	}

	project := fs.Lookup("project").Value.String()
	home, err := watchHome(*homeFlag, project)
	if err != nil {
		return err
	}
	name := raglit.NormalizeIndexName(*index)
	added, err := addWatchRoot(home, name, dir, project)
	if err != nil {
		return err
	}
	if added {
		fmt.Printf("added %s to index %q in %s\n", dir, name, home.ConfigPath())
	} else {
		fmt.Printf("%s is already in index %q\n", dir, name)
	}

	sync := []string{"--home", string(home), "--index", name}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "daemon" || f.Name == "project" {
			sync = append(sync, "--"+f.Name, f.Value.String())
		}
	})
	return runSync(sync)
}

// watchHome is the config that records a watched root: --home, else the
// nearest ./.raglit, else a home of the project's own under the daemon root
// (<root>/projects/<project>), for a caller with no .raglit (dun). Never the
// shared default home: every project without a .raglit would write its roots
// into one config.
func watchHome(homeFlag, project string) (raglit.Home, error) {
	if homeFlag != "" {
		return raglit.Home(homeFlag), nil
	}
	if h := raglit.DiscoverHome(); h != raglit.DefaultHome() {
		return h, nil
	}
	if strings.TrimSpace(project) == "" {
		return "", fmt.Errorf("no ./.raglit here and no --project: run in a project (raglit init) or pass --project NAME")
	}
	return raglit.Home(filepath.Join(raglit.DefaultRoot(), "projects", raglit.NormalizeIndexName(project))), nil
}

// projectDirOf is the directory a project home belongs to (the one holding
// .raglit), or "" for a home that is no project's: the shared default home, or
// one watchHome made under the daemon root.
func projectDirOf(h raglit.Home) string {
	if h == "" || h == raglit.DefaultHome() {
		return ""
	}
	abs, err := filepath.Abs(string(h))
	if err != nil || strings.HasPrefix(abs, filepath.Join(raglit.DefaultRoot(), "projects")+string(filepath.Separator)) {
		return ""
	}
	return filepath.Dir(abs)
}

// addWatchRoot records dir as a root of index name and turns watching on. It
// reports whether the config changed. A config that does not exist yet is
// created, carrying project when one was given, so a caller with no config of
// its own (dun) can start here.
func addWatchRoot(home raglit.Home, name, dir, project string) (bool, error) {
	cfg, ok, err := raglit.LoadConfig(home)
	if err != nil {
		return false, err
	}
	if !ok && project != "" {
		cfg.Project = project
	}
	baseDir := filepath.Dir(string(home))
	ic := cfg.Indexes[name]
	for _, r := range ic.Roots {
		if rootDir(baseDir, r.Path) == dir {
			if cfg.Watch && ok {
				return false, nil
			}
			cfg.Watch = true
			return false, raglit.SaveConfig(home, cfg)
		}
	}
	ic.Roots = append(ic.Roots, raglit.Root{Path: rootPath(baseDir, dir)})
	if cfg.Indexes == nil {
		cfg.Indexes = map[string]raglit.IndexConfig{}
	}
	cfg.Indexes[name] = ic
	cfg.Watch = true
	return true, raglit.SaveConfig(home, cfg)
}

// removeWatchRoot drops dir from index name's roots and reports whether it was
// there. The index keeps its documents.
func removeWatchRoot(home raglit.Home, name, dir string) (bool, error) {
	cfg, ok, err := raglit.LoadConfig(home)
	if err != nil || !ok {
		return false, err
	}
	baseDir := filepath.Dir(string(home))
	ic, found := cfg.Indexes[name]
	if !found {
		return false, nil
	}
	kept := ic.Roots[:0]
	removed := false
	for _, r := range ic.Roots {
		if rootDir(baseDir, r.Path) == dir {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	if !removed {
		return false, nil
	}
	ic.Roots = kept
	cfg.Indexes[name] = ic
	return true, raglit.SaveConfig(home, cfg)
}

// rootDir resolves a configured root the way PlanSources does: relative to the
// directory holding .raglit.
func rootDir(baseDir, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	return filepath.Clean(p)
}

// rootPath is how a new root is written: relative when it lies inside the
// project, so a config checked into the project still works from another
// checkout, and absolute otherwise.
func rootPath(baseDir, dir string) string {
	if rel, err := filepath.Rel(baseDir, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return dir
}
