package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/iodesystems/raglit"
)

// runSync reads the project config's `indexes` (roots + include/ignore rules),
// resolves them to concrete files (respecting .gitignore + the built-in default
// ignore), and enqueues each into its index. Unchanged files are skipped by the
// content-hash dedup, so re-syncing is cheap. Routes to a daemon when one is
// configured, else the local home.
func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	daemon := addDaemonFlag(fs)
	only := fs.String("index", "", "sync only this configured index (default: all)")
	dry := fs.Bool("dry-run", false, "print what would be ingested; don't enqueue")
	fs.Parse(args)

	homeOf := func() raglit.Home {
		if *homeFlag != "" {
			return raglit.Home(*homeFlag)
		}
		return raglit.DiscoverHome()
	}

	home := homeOf()
	cfg, ok, err := raglit.LoadConfig(home)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no config at %s — run `raglit init`", home.ConfigPath())
	}
	if len(cfg.Indexes) == 0 {
		return fmt.Errorf(`config has no "indexes" to sync — add roots + include/ignore to %s`, home.ConfigPath())
	}

	// Roots resolve against the project directory (the one holding .raglit), so
	// `raglit sync` is reproducible from any subdirectory.
	baseDir := filepath.Dir(string(home))
	plan, err := raglit.PlanSources(cfg, baseDir)
	if err != nil {
		return err
	}

	dURL := resolveDaemon(*daemon, homeOf)
	names := make([]string, 0, len(plan))
	for n := range plan {
		names = append(names, n)
	}
	sort.Strings(names)

	total := 0
	for _, name := range names {
		if *only != "" && name != *only {
			continue
		}
		files := plan[name]
		fmt.Printf("index %q: %d file(s)\n", name, len(files))
		if *dry {
			for _, f := range files {
				fmt.Println("  ", f)
			}
			continue
		}
		if dURL != "" {
			if err := daemonIngest(dURL, files, name, ""); err != nil {
				return err
			}
		} else {
			st, err := raglit.OpenIndex(home, name)
			if err != nil {
				return err
			}
			for _, f := range files {
				if _, err := st.Enqueue(f, ""); err != nil {
					st.Close()
					return err
				}
			}
			st.Close()
		}
		total += len(files)
	}
	if !*dry {
		hint := "drain with `raglit work`"
		if dURL != "" {
			hint = "the daemon drains it in the background"
		}
		fmt.Printf("queued %d file(s) across %d index(es) — %s.\n", total, len(names), hint)
	}
	return nil
}
