package raglit

import (
	"os"
	"sort"
	"strings"
	"sync"
)

// Registry manages the set of NAMED indexes in one home. Each index is its own
// sqlite file sharing the home's originals/ and pages/: "default" is
// index.sqlite, any other name is index-<name>.sqlite. A serve process opens a
// Registry so its search/ingest/status tools can target — or fan out across —
// several indexes (Slice G).
type Registry struct {
	home     Home
	mu       sync.Mutex
	stores   map[string]*Store
	embedder *Embedder
}

// OpenRegistry prepares a registry over a home (creating the layout).
func OpenRegistry(home Home) (*Registry, error) {
	if err := home.Ensure(); err != nil {
		return nil, err
	}
	return &Registry{home: home, stores: map[string]*Store{}}, nil
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
	s, err := OpenIndex(r.home, name)
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
	if entries, err := os.ReadDir(string(r.home)); err == nil {
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
