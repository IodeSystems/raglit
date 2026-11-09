package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// treeTarget is one configured root indexed into one daemon index: what
// `sync` and the watcher hand to raglit.IndexTree.
type treeTarget struct {
	dir   string // absolute root directory
	index string // daemon index (namespaced)
	local string // the config's own index name
	rules raglit.TreeRules
	sig   string // identity of dir+index+rules, to notice a config edit
}

// configTargets resolves a project config's indexes to tree targets, the way
// PlanSources resolves them: a root's include overrides its index's, and the
// ignores of config, index and root add up. only, when set, keeps one index.
func configTargets(cfg raglit.Config, baseDir, ns, only string) []treeTarget {
	var out []treeTarget
	for name, idx := range cfg.Indexes {
		if only != "" && name != only {
			continue
		}
		for _, root := range idx.Roots {
			include := root.Include
			if len(include) == 0 {
				include = idx.Include
			}
			var ignore []string
			ignore = append(append(append(ignore, cfg.Ignore...), idx.Ignore...), root.Ignore...)
			t := treeTarget{
				dir:   rootDir(baseDir, root.Path),
				index: nsIndex(ns, name),
				local: name,
				rules: raglit.TreeRules{Include: include, Ignore: ignore, Gitignore: cfg.Gitignore},
			}
			git := "on"
			if cfg.Gitignore != nil && !*cfg.Gitignore {
				git = "off"
			}
			t.sig = strings.Join([]string{t.dir, t.index, strings.Join(include, ","), strings.Join(ignore, ","), git}, "\x00")
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sig < out[j].sig })
	return slices.CompactFunc(out, func(a, b treeTarget) bool { return a.sig == b.sig })
}

// noLinkOut is a Skip that drops a path whose real location is not under
// real (a resolved root): a link out of a granted directory is not followed.
func noLinkOut(real string) func(string) bool {
	return func(p string) bool {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return true
		}
		return r != real && !strings.HasPrefix(r, real+string(filepath.Separator))
	}
}

// dirTarget is a watched directory as a tree target: no project config, and
// no link out of the directory.
func dirTarget(d dirWatch) (treeTarget, error) {
	real, err := filepath.EvalSymlinks(d.Dir)
	if err != nil {
		return treeTarget{}, err
	}
	return treeTarget{
		dir: d.Dir, index: d.Index, local: d.Index,
		rules: raglit.TreeRules{Skip: noLinkOut(real)},
		sig:   d.key(),
	}, nil
}

// treeLine is a TreeResult as one log or output line.
func treeLine(r raglit.TreeResult) string {
	s := fmt.Sprintf("queued %d, current %d, removed %d, failed %d, skipped %d",
		r.Queued, r.Current, r.Removed, r.Failed, r.Skipped)
	if len(r.Errors) > 0 {
		s += fmt.Sprintf(", %d error(s)", len(r.Errors))
	}
	return s
}

func treeChanged(r raglit.TreeResult) bool {
	return r.Queued > 0 || r.Removed > 0 || len(r.Errors) > 0
}
