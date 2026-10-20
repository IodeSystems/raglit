package main

import (
	"context"

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
// Known edge: a layer searched own-only no longer hides the parent documents it
// tombstoned or shadows; both versions can appear, each labelled by index.

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
	selected := reg.Readable(ctx, selectIndexes(reg, sel))
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
