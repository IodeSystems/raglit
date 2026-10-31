package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/iodesystems/raglit"
)

// Which indexes a search reads, and how.
//
// A branch search fans out to its readable siblings (USER 2026-10-19: a general
// branch feature; ownership is the ACL on top). A branch already overlays its
// parent, so every index whose parent is covered by another index in the set
// is searched as its OWN LAYER only — otherwise the parent's documents would be
// counted once per layer that overlays them.
//
// A layer searched own-only does not hide the parent documents it tombstoned or
// shadows. That is correct: the parent's copy reaches the results only through
// another index in the set that does show it, so both versions appear, each
// labelled by index.

type searchPlan struct {
	names []string
	own   map[string]bool // search this index's own layer only
}

func planSearch(ctx context.Context, reg *raglit.Registry, sel string, siblings bool) searchPlan {
	p := searchPlan{own: map[string]bool{}}
	in := map[string]bool{}
	add := func(n string) bool {
		if in[n] {
			return false
		}
		in[n] = true
		p.names = append(p.names, n)
		return true
	}
	selected := reg.Readable(ctx, selectIndexes(ctx, reg, sel))
	for _, n := range selected {
		add(n)
	}
	if siblings {
		for _, n := range selected {
			for _, s := range reg.Siblings(ctx, n) {
				if add(s) {
					p.own[s] = true // n's overlay already covers the parent
				}
			}
		}
	}
	// An ancestor in the set already contributes the parent's documents.
	coveredParent := map[string]bool{}
	for _, n := range p.names {
		if p.own[n] {
			continue
		}
		for a, depth := reg.ParentOf(n), 0; a != "" && depth < 64; a, depth = reg.ParentOf(a), depth+1 {
			if in[a] {
				p.own[n] = true
				break
			}
		}
		// Siblings selected together, parent not selected: the first keeps its
		// overlay, the rest read their own layers.
		if parent := reg.ParentOf(n); parent != "" && !p.own[n] {
			if coveredParent[parent] {
				p.own[n] = true
			}
			coveredParent[parent] = true
		}
	}
	return p
}

// siblingsOn reads the `siblings` parameter: on unless "false"/"0"/"no".
func siblingsOn(v string) bool {
	switch v {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// missingNamed returns the members of a selector that name one index (no
// wildcard, no "@cwd:") which does not exist, each with the indexes the caller
// may read whose local name matches, as "did you mean" candidates. A search
// that names an index which is not there is a mistake to report, not an empty
// result: zero hits read exactly like "nothing about that".
func missingNamed(ctx context.Context, reg *raglit.Registry, sel string) map[string][]string {
	sel = strings.TrimSpace(sel)
	if sel == "" || sel == "all" {
		return nil
	}
	var out map[string][]string
	names := reg.Names()
	for _, n := range strings.Split(sel, ",") {
		n = strings.TrimSpace(n)
		if n == "" || strings.HasSuffix(n, "*") || strings.HasPrefix(n, "@cwd:") {
			continue
		}
		resolved := uniqueLocal(names, n)
		if len(reg.Readable(ctx, []string{resolved})) > 0 {
			continue
		}
		local := resolved
		if i := strings.LastIndex(local, nsSep); i >= 0 {
			local = local[i+len(nsSep):]
		}
		var like []string
		for _, c := range reg.Readable(ctx, names) {
			if c == "default" {
				continue
			}
			cl := c
			if i := strings.LastIndex(cl, nsSep); i >= 0 {
				cl = cl[i+len(nsSep):]
			}
			if cl == local || strings.Contains(cl, local) || strings.Contains(local, cl) {
				like = append(like, c)
			}
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[n] = like
	}
	return out
}

// missingIndexError is the 404 for missingNamed's result.
func missingIndexError(missing map[string][]string) error {
	keys := make([]string, 0, len(missing))
	for k := range missing {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "no index %q", k)
		if like := missing[k]; len(like) > 0 {
			sels := make([]string, len(like))
			for i, c := range like {
				sels[i] = selectorOf(c)
			}
			fmt.Fprintf(&b, " (did you mean %s?)", strings.Join(sels, ", "))
		}
	}
	return huma.Error404NotFound(b.String())
}

// selectorOf spells a daemon index name the way a client selects it:
// "proj__idx" is "proj:idx", a global "idx" is ":idx".
func selectorOf(name string) string {
	if p, l, ok := strings.Cut(name, nsSep); ok {
		return p + ":" + l
	}
	return ":" + name
}
