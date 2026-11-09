package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"os"
	"path/filepath"
	"sort"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// runSync brings each configured index in step with its roots: the daemon
// runs raglit.IndexTree per root (POST /api/sync), which queues new and
// changed files, counts unchanged ones as current, and removes documents
// whose files are gone or no longer selected. --dry-run lists the planned
// files locally and changes nothing.
func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	client := addClientFlags(fs) // --daemon + --project
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

	var dURL, ns string
	if !*dry {
		dURL, ns, err = client(homeOf)
		if err != nil {
			return err
		}
	}
	names := make([]string, 0, len(plan))
	for n := range plan {
		names = append(names, n)
	}
	sort.Strings(names)

	if *dry {
		for _, name := range names {
			if *only != "" && name != *only {
				continue
			}
			files := plan[name]
			fmt.Printf("index %q: %d file(s)\n", name, len(files))
			for _, f := range files {
				fmt.Println("  ", f)
			}
		}
		return nil
	}
	absHome, err := filepath.Abs(string(home))
	if err != nil {
		return err
	}
	b, err := daemonPostJSON(dURL, "/api/sync", map[string]any{
		"home": absHome, "project": ns, "index": *only, "scope": projectDirOf(home),
	})
	if err != nil {
		return err
	}
	var out syncResult
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	var total raglit.TreeResult
	for _, ix := range out.Indexes {
		r := ix.result()
		fmt.Printf("index %q: %s\n", ix.Index, treeLine(r))
		for _, e := range ix.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
		total.Add(r)
	}
	fmt.Printf("queued %d file(s), removed %d document(s) across %d index(es) — the daemon drains the queue in the background.\n",
		total.Queued, total.Removed, len(out.Indexes))

	// watch:true → register with the daemon so it keeps re-ingesting on change.
	if cfg.Watch {
		if _, werr := daemonPostJSON(dURL, "/api/watch", map[string]any{"home": absHome}); werr == nil {
			fmt.Printf("watching %s for changes (config watch:true)\n", absHome)
		} else {
			fmt.Fprintf(os.Stderr, "warning: could not register watch: %v\n", werr)
		}
	}
	return nil
}

// syncIndex is one index's IndexTree counts, summed over its roots.
type syncIndex struct {
	Index   string   `json:"index"`
	Queued  int      `json:"queued"`
	Current int      `json:"current"`
	Removed int      `json:"removed"`
	Failed  int      `json:"failed"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors"`
}

func (s syncIndex) result() raglit.TreeResult {
	r := raglit.TreeResult{Queued: s.Queued, Current: s.Current, Removed: s.Removed, Failed: s.Failed, Skipped: s.Skipped}
	for _, e := range s.Errors {
		r.Errors = append(r.Errors, fmt.Errorf("%s", e))
	}
	return r
}

type syncResult struct {
	Indexes []syncIndex `json:"indexes"`
}

type syncIn struct {
	Body struct {
		Home    string `json:"home"`
		Project string `json:"project"`
		Index   string `json:"index,omitempty"`
		Scope   string `json:"scope,omitempty"`
	}
}

type syncOut struct {
	Body syncResult
}

// syncErrorsShown caps the per-file errors returned per index.
const syncErrorsShown = 20

// syncOp is the daemon side of `raglit sync`: IndexTree over each configured
// root of the home's config. The caller must be allowed to read the home and
// each root on this host; a caller that is not the admin also gets no file
// reached through a link out of its granted roots.
func syncOp(reg *raglit.Registry) func(context.Context, *syncIn) (*syncOut, error) {
	return func(ctx context.Context, in *syncIn) (*syncOut, error) {
		home := strings.TrimSpace(in.Body.Home)
		if home == "" || !filepath.IsAbs(home) {
			return nil, huma.Error400BadRequest("home must be an absolute path")
		}
		if err := requireHostPaths(ctx, reg, []string{home}); err != nil {
			return nil, err
		}
		cfg, ok, err := raglit.LoadConfig(raglit.Home(home))
		if err != nil || !ok {
			return nil, huma.Error400BadRequest(fmt.Sprintf("no config at %s", home))
		}
		ns := strings.TrimSpace(in.Body.Project)
		if ns == "" {
			ns = cfg.Project
		}
		if ns == "" {
			return nil, huma.Error400BadRequest("project is required")
		}
		ns = raglit.NormalizeIndexName(ns)
		targets := configTargets(cfg, filepath.Dir(home), ns, in.Body.Index)
		dirs := make([]string, 0, len(targets))
		for _, t := range targets {
			dirs = append(dirs, t.dir)
		}
		if err := requireHostPaths(ctx, reg, dirs); err != nil {
			return nil, err
		}
		admin := requireAdmin(ctx, reg) == nil
		byIndex := map[string]*raglit.TreeResult{}
		var order []string
		for _, t := range targets {
			st, err := writeIdx(ctx, reg, t.index)
			if err != nil {
				return nil, err
			}
			if in.Body.Scope != "" {
				if err := applyScope(st, nil, nil, nil, nil, in.Body.Scope); err != nil {
					return nil, huma.Error500InternalServerError("scope", err)
				}
			}
			rules := t.rules
			if !admin {
				rules.Skip = func(p string) bool { return !hostPathAllowed(ctx, reg, p) }
			}
			res, err := raglit.IndexTree(ctx, st, t.dir, raglit.TreeOptions{Rules: rules, Keep: keepFor(t, targets)})
			if err != nil {
				return nil, huma.Error400BadRequest(fmt.Sprintf("sync %s: %v", t.dir, err))
			}
			if byIndex[t.local] == nil {
				byIndex[t.local] = &raglit.TreeResult{}
				order = append(order, t.local)
			}
			byIndex[t.local].Add(res)
		}
		sort.Strings(order)
		out := &syncOut{}
		out.Body.Indexes = []syncIndex{}
		for _, name := range order {
			r := byIndex[name]
			si := syncIndex{Index: name, Queued: r.Queued, Current: r.Current, Removed: r.Removed,
				Failed: r.Failed, Skipped: r.Skipped, Errors: []string{}}
			for i, e := range r.Errors {
				if i == syncErrorsShown {
					si.Errors = append(si.Errors, fmt.Sprintf("… and %d more", len(r.Errors)-i))
					break
				}
				si.Errors = append(si.Errors, e.Error())
			}
			out.Body.Indexes = append(out.Body.Indexes, si)
		}
		return out, nil
	}
}
