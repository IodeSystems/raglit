package raglit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Registry manages a SET of named indexes. Two storage layouts:
//
//   - single-home (OpenRegistry): all indexes live in one Home as sibling sqlite
//     files sharing its originals/ + pages/ — "default" is index.sqlite, any other
//     name is index-<name>.sqlite. This is the embedded/project layout.
//   - scoped (OpenScopedRegistry): each index is its OWN Home under
//     <root>/indexes/<name>/ (own index.sqlite + originals/ + pages/), fully
//     isolated. This is the DAEMON's multi-index layout — and the substrate for
//     branch storage (a branch is a scoped store layered over a parent).
type Registry struct {
	home       Home   // single-home mode
	scopedRoot string // scoped mode: indexes at <scopedRoot>/indexes/<name>; "" = single-home
	mu         sync.Mutex
	stores     map[string]*Store
	embedder   *Embedder
}

// OpenRegistry prepares a single-home registry (all indexes as sqlite siblings
// in one home).
func OpenRegistry(home Home) (*Registry, error) {
	if err := home.Ensure(); err != nil {
		return nil, err
	}
	return &Registry{home: home, stores: map[string]*Store{}}, nil
}

// OpenScopedRegistry prepares a registry whose indexes are each their own Home
// under <root>/indexes/<name>/ — the daemon's scoped, per-index storage.
func OpenScopedRegistry(root string) (*Registry, error) {
	if err := os.MkdirAll(filepath.Join(root, "indexes"), 0o755); err != nil {
		return nil, fmt.Errorf("raglit: create scoped root: %w", err)
	}
	return &Registry{scopedRoot: root, stores: map[string]*Store{}}, nil
}

// indexHome is the scoped Home for a named index (scoped mode only).
func (r *Registry) indexHome(name string) Home {
	return Home(filepath.Join(r.scopedRoot, "indexes", name))
}

// SetEmbedder makes every index (already open or opened later) embed on ingest.
func (r *Registry) SetEmbedder(e *Embedder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.embedder = e
	for _, s := range r.stores {
		s.SetEmbedder(e)
	}
}

// Get opens (once, then caches) the named index, creating it if absent.
func (r *Registry) Get(name string) (*Store, error) {
	name = normalizeIndexName(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.stores[name]; ok {
		return s, nil
	}
	var s *Store
	var err error
	if r.scopedRoot != "" {
		// Each index is its own Home's primary index (index.sqlite).
		s, err = OpenHome(r.indexHome(name))
	} else {
		s, err = OpenIndex(r.home, name)
	}
	if err != nil {
		return nil, err
	}
	if r.embedder != nil {
		s.SetEmbedder(r.embedder)
	}
	r.stores[name] = s
	return s, nil
}

// Names lists the indexes present on disk (always including "default"), plus any
// opened this session, sorted.
func (r *Registry) Names() []string {
	set := map[string]bool{"default": true}
	if r.scopedRoot != "" {
		// Scoped: each subdir of <root>/indexes/ is an index.
		if entries, err := os.ReadDir(filepath.Join(r.scopedRoot, "indexes")); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					set[e.Name()] = true
				}
			}
		}
	} else if entries, err := os.ReadDir(string(r.home)); err == nil {
		for _, e := range entries {
			n := e.Name()
			if strings.HasPrefix(n, "index-") && strings.HasSuffix(n, ".sqlite") {
				set[strings.TrimSuffix(strings.TrimPrefix(n, "index-"), ".sqlite")] = true
			}
		}
	}
	r.mu.Lock()
	for n := range r.stores {
		set[n] = true
	}
	r.mu.Unlock()
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Close releases every open index.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for _, s := range r.stores {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	r.stores = map[string]*Store{}
	return firstErr
}
