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
	"fmt"
	"strings"

	"github.com/iodesystems/raglit"
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

// nsUnmatchable is a selector that matches no index (a wildcard whose prefix
// can't occur in a normalized [a-z0-9_-] index name). Used when an explicit
// selection resolves to nothing allowed, so it yields zero hits rather than
// falling through to the daemon's "empty = all".
const nsUnmatchable = "\x00*"

// nsResolveToken maps one index token to a daemon index name, honoring
// "<namespace>:<local>" addressing:
//
//	"code"            → "<own>__code"           (bare → this project)
//	"shared:handbook" → "shared__handbook"      (if "shared" is own or declared shared)
//	"shared:*"        → "shared__*"             (every index in that namespace)
//	"other:secret"    → rejected (ok=false)     (namespace not addressable here)
//
// Restricting the namespace to the project itself or its declared `shared`
// entries preserves isolation — a project can't reach an arbitrary project's
// indexes by guessing "<name>:<index>". ns=="" (embedded) passes the token
// through unchanged.
func nsResolveToken(ownNS string, shared []string, token string) (string, bool) {
	token = strings.TrimSpace(token)
	if ownNS == "" {
		return token, true
	}
	nsPart, local, addressed := strings.Cut(token, ":")
	if !addressed {
		return ownNS + nsSep + raglit.NormalizeIndexName(token), true
	}
	tgt := raglit.NormalizeIndexName(nsPart)
	if tgt != ownNS && !containsStr(shared, tgt) {
		return "", false
	}
	if strings.TrimSpace(local) == "*" {
		return tgt + nsSep + "*", true
	}
	return tgt + nsSep + raglit.NormalizeIndexName(local), true
}

// nsWriteIndex resolves a single write target (ingest), erroring if the token
// addresses a namespace this project may not reach.
func nsWriteIndex(ownNS string, shared []string, token string) (string, error) {
	d, ok := nsResolveToken(ownNS, shared, token)
	if !ok {
		return "", fmt.Errorf("index %q: namespace not addressable from this project (own project or a `shared` entry only)", token)
	}
	return d, nil
}

// nsReadSelector maps a local READ selector to a daemon selector spanning this
// project plus its shared namespaces. Empty/"all" → "<ns>__*" plus "<shared>__*"
// for each shared namespace. An explicit selection is a comma-separated list of
// tokens, each resolved via nsResolveToken ("<ns>:<local>" addressing allowed for
// own + shared); tokens naming an unreachable namespace are dropped, and if none
// survive the selector matches nothing (never "all"). ns=="" returns sel as-is.
func nsReadSelector(ns string, shared []string, sel string) string {
	if ns == "" {
		return strings.TrimSpace(sel)
	}
	s := strings.TrimSpace(sel)
	if s == "" || s == "all" {
		parts := []string{ns + nsSep + "*"}
		for _, sh := range shared { // shared is pre-normalized (see projectShared)
			if sh != "" && sh != ns {
				parts = append(parts, sh+nsSep+"*")
			}
		}
		return strings.Join(parts, ",")
	}
	var parts []string
	for _, tok := range strings.Split(s, ",") {
		if tok = strings.TrimSpace(tok); tok == "" {
			continue
		}
		if d, ok := nsResolveToken(ns, shared, tok); ok {
			parts = append(parts, d)
		}
	}
	if len(parts) == 0 {
		return nsUnmatchable // explicit selection, nothing reachable → no hits
	}
	return strings.Join(parts, ",")
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
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
