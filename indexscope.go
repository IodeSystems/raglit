package raglit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// Index scopes: which directories an index applies in (USER 2026-10-23).
//
// A search with no index named reads the indexes whose scopes match the
// directory it was run from, plus the ambient global ones. An index with no
// scopes is global: ambient (every search reads it) or opt-in (read only when
// named). Stored in the index, like pause, so every client sees one answer.

const (
	metaScopes  = "scopes"
	metaAmbient = "ambient"
)

// Scopes is this index's directory patterns. set is false when they were never
// written, which reads as an opt-in global index and is what the daemon's
// one-time migration and an ingest's scope claim look for.
func (s *Store) Scopes() (scopes []string, set bool) {
	v, ok := s.Meta(metaScopes)
	if !ok {
		return []string{}, false
	}
	var out []string
	if json.Unmarshal([]byte(v), &out) != nil || out == nil {
		return []string{}, true
	}
	return out, true
}

// SetScopes replaces this index's directory patterns. Blank entries and repeats
// are dropped; a plain directory is cleaned. An empty list makes the index
// global.
func (s *Store) SetScopes(scopes []string) ([]string, error) {
	seen := map[string]bool{}
	clean := []string{}
	for _, p := range scopes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.ContainsAny(p, "*?") {
			p = filepath.Clean(p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		clean = append(clean, p)
	}
	b, _ := json.Marshal(clean)
	return clean, s.SetMeta(metaScopes, string(b), time.Now().Unix())
}

// Ambient reports whether a global index is read by every search.
func (s *Store) Ambient() bool {
	v, _ := s.Meta(metaAmbient)
	return v == "1"
}

// SetAmbient makes a global index ambient (true) or opt-in (false).
func (s *Store) SetAmbient(ambient bool) error {
	v := ""
	if ambient {
		v = "1"
	}
	return s.SetMeta(metaAmbient, v, time.Now().Unix())
}

// ScopeMatches reports whether dir lies in one of scopes. A plain directory
// matches itself and everything under it; a pattern with * or ? is a glob in
// the ignore-pattern syntax (** crosses directories), matched against dir.
func ScopeMatches(scopes []string, dir string) bool {
	dir = filepath.Clean(dir)
	for _, p := range scopes {
		if strings.ContainsAny(p, "*?") {
			if globRegexp(p).MatchString(dir) {
				return true
			}
			continue
		}
		if dir == p || strings.HasPrefix(dir, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

// ScopedFor is the indexes a search from dir reads when it names none: those
// whose scopes match dir, and the ambient global ones. Only indexes the caller
// may read.
func (r *Registry) ScopedFor(ctx context.Context, dir string) []string {
	var out []string
	for _, name := range r.Readable(ctx, r.Names()) {
		if r.scopedRoot != "" && !r.exists(name) {
			continue // "default" is always listed, and opening it would create it
		}
		st, err := r.Existing(name)
		if err != nil {
			continue
		}
		scopes, _ := st.Scopes()
		if len(scopes) > 0 && ScopeMatches(scopes, dir) || len(scopes) == 0 && st.Ambient() {
			out = append(out, name)
		}
	}
	return out
}
