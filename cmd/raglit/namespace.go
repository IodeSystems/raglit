package main

// Project namespacing (cross-project isolation on the shared daemon).
//
// One per-user daemon serves every project, and it's project-agnostic: it just
// opens indexes by name. Without namespacing, every project's default index is
// literally "default", so their documents pile into one <root>/indexes/default,
// and a project's "search all" (empty index) returns EVERY project's indexes.
//
// The client fixes both by prefixing its project name onto every index name it
// sends: daemon index = "<project>__<local>". "Search all" becomes the wildcard
// "<project>__*", which the daemon resolves to only that project's indexes. The
// project name is required to start a daemon-routed client (see addClientFlags),
// so this prefix is always present. On the way back, the "<project>__" prefix is
// stripped from responses so the user/agent sees local names.

import (
	"encoding/json"
	"strings"
)

// nsSep separates the project namespace from the local index name in a daemon
// index name. Both halves are normalized to [a-z0-9_-]; the double underscore
// keeps the split unambiguous for display stripping (a TrimPrefix of "<ns>__").
const nsSep = "__"

// nsIndex maps a single project-local index name to its daemon name. Empty local
// → "default". ns=="" (embedded, no daemon) returns local unchanged.
func nsIndex(ns, local string) string {
	local = strings.TrimSpace(local)
	if local == "" {
		local = "default"
	}
	if ns == "" {
		return local
	}
	return ns + nsSep + local
}

// nsSelector maps a local index SELECTOR (search/status/list) to a daemon
// selector: empty/"all" → "<ns>__*" (all of THIS project's indexes); a
// comma-separated set → each name prefixed. ns=="" returns the selector as-is.
func nsSelector(ns, sel string) string {
	sel = strings.TrimSpace(sel)
	if ns == "" {
		return sel
	}
	if sel == "" || sel == "all" {
		return ns + nsSep + "*"
	}
	parts := strings.Split(sel, ",")
	for i, p := range parts {
		parts[i] = ns + nsSep + strings.TrimSpace(p)
	}
	return strings.Join(parts, ",")
}

// nsReadSelector maps a local READ selector to a daemon selector that spans this
// project plus its shared namespaces. An explicit selection stays private (each
// name prefixed with the project ns). The "all" case (empty/"all") becomes
// "<ns>__*" plus "<shared>__*" for each shared namespace — so shared docs indexed
// once (e.g. under a "shared" project) are searched from every project that opts
// in. ns=="" (embedded) returns the selector unchanged.
func nsReadSelector(ns string, shared []string, sel string) string {
	if ns == "" {
		return strings.TrimSpace(sel)
	}
	if s := strings.TrimSpace(sel); s != "" && s != "all" {
		return nsSelector(ns, s) // explicit → private to this project
	}
	parts := []string{ns + nsSep + "*"}
	for _, s := range shared { // shared is pre-normalized (see projectShared)
		if s != "" && s != ns {
			parts = append(parts, s+nsSep+"*")
		}
	}
	return strings.Join(parts, ",")
}

// nsStrip removes the "<ns>__" prefix from a daemon index name for display.
func nsStrip(ns, full string) string {
	if ns == "" {
		return full
	}
	return strings.TrimPrefix(full, ns+nsSep)
}

// stripNSJSON rewrites every "index" string field in a JSON document, removing
// the "<ns>__" prefix, so proxied daemon responses show local index names. Other
// fields are untouched; malformed JSON is returned unchanged.
func stripNSJSON(b []byte, ns string) []byte {
	if ns == "" {
		return b
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	walkStripIndex(v, ns+nsSep)
	if out, err := json.Marshal(v); err == nil {
		return out
	}
	return b
}

func walkStripIndex(v any, prefix string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "index" {
				if s, ok := val.(string); ok {
					t[k] = strings.TrimPrefix(s, prefix)
					continue
				}
			}
			walkStripIndex(val, prefix)
		}
	case []any:
		for _, e := range t {
			walkStripIndex(e, prefix)
		}
	}
}

// filterIndexList narrows a list_indexes response ({"indexes":[{"name",...}]}) to
// this project's indexes plus any shared namespaces it reads. The project's own
// "<ns>__" prefix is stripped for display; shared rows keep their "<shared>__"
// name so their provenance is visible. Everything else is dropped.
func filterIndexList(b []byte, ns string, shared []string) []byte {
	if ns == "" {
		return b
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return b
	}
	rows, ok := doc["indexes"].([]any)
	if !ok {
		return b
	}
	own := ns + nsSep
	kept := make([]any, 0, len(rows))
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		switch {
		case strings.HasPrefix(name, own):
			m["name"] = strings.TrimPrefix(name, own)
			kept = append(kept, m)
		case hasAnyPrefix(name, shared, nsSep):
			kept = append(kept, m) // shared: keep the "<shared>__name" for provenance
		}
	}
	doc["indexes"] = kept
	if out, err := json.Marshal(doc); err == nil {
		return out
	}
	return b
}

// hasAnyPrefix reports whether name starts with "<p><sep>" for any p in ps.
func hasAnyPrefix(name string, ps []string, sep string) bool {
	for _, p := range ps {
		if p != "" && strings.HasPrefix(name, p+sep) {
			return true
		}
	}
	return false
}
