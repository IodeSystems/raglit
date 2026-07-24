package raglit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
	return r.getLocked(name)
}

// getLocked opens (once, then caches) the named index; the caller holds r.mu. In
// scoped mode a branch (it has a branch.json) is wired to its parent so reads
// overlay branch-over-parent.
func (r *Registry) getLocked(name string) (*Store, error) {
	if s, ok := r.stores[name]; ok {
		return s, nil
	}
	var s *Store
	var err error
	if r.scopedRoot != "" {
		home := r.indexHome(name)
		s, err = OpenHome(home)
		if err != nil {
			return nil, err
		}
		if meta, ok := readBranchMeta(home); ok {
			if p, perr := r.getLocked(normalizeIndexName(meta.Parent)); perr == nil {
				s.SetParent(p)
			}
		}
	} else {
		s, err = OpenIndex(r.home, name)
		if err != nil {
			return nil, err
		}
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

// ── branch operations (scoped registries only) ──────────────────────────

// BranchInfo describes a branch for listing: lineage, age, last access, and its
// LOCAL document count (the branch's own diffs over the parent).
type BranchInfo struct {
	Name           string `json:"name"`
	Parent         string `json:"parent"`
	CreatedAt      int64  `json:"created_at"`
	LastAccessedAt int64  `json:"last_accessed_at"`
	Documents      int    `json:"documents"`
}

// ForkBranch creates a branch index `name` whose reads overlay `parent`
// (copy-on-write). Errors if the name is taken, the parent is unknown, or this
// is not a scoped registry.
func (r *Registry) ForkBranch(name, parent string) error {
	if r.scopedRoot == "" {
		return fmt.Errorf("raglit: branches require a scoped daemon (no --home)")
	}
	name, parent = normalizeIndexName(name), normalizeIndexName(parent)
	if name == parent {
		return fmt.Errorf("raglit: a branch cannot be its own parent")
	}
	home := r.indexHome(name)
	if _, ok := readBranchMeta(home); ok {
		return fmt.Errorf("raglit: branch %q already exists", name)
	}
	if fi, err := os.Stat(home.IndexPath()); err == nil && fi.Size() > 0 {
		return fmt.Errorf("raglit: index %q already exists (not a branch)", name)
	}
	// Parent must resolve.
	if _, err := r.Get(parent); err != nil {
		return fmt.Errorf("raglit: parent %q: %w", parent, err)
	}
	if err := home.Ensure(); err != nil {
		return err
	}
	now := time.Now().UnixNano()
	if err := writeBranchMeta(home, BranchMeta{Parent: parent, CreatedAt: now, LastAccessedAt: now}); err != nil {
		return err
	}
	_, err := r.Get(name) // open now so the overlay is wired
	return err
}

// DeleteBranch closes and removes a branch (its scoped storage), leaving the
// parent untouched. Errors if `name` is not a branch.
func (r *Registry) DeleteBranch(name string) error {
	if r.scopedRoot == "" {
		return fmt.Errorf("raglit: branches require a scoped daemon (no --home)")
	}
	name = normalizeIndexName(name)
	home := r.indexHome(name)
	if _, ok := readBranchMeta(home); !ok {
		return fmt.Errorf("raglit: %q is not a branch", name)
	}
	r.mu.Lock()
	if s, ok := r.stores[name]; ok {
		s.Close()
		delete(r.stores, name)
	}
	r.mu.Unlock()
	return os.RemoveAll(string(home))
}

// ListBranches enumerates the scoped registry's branches with lineage + age +
// last-access + local doc count, sorted by name.
func (r *Registry) ListBranches() ([]BranchInfo, error) {
	if r.scopedRoot == "" {
		return []BranchInfo{}, nil
	}
	entries, err := os.ReadDir(filepath.Join(r.scopedRoot, "indexes"))
	if err != nil {
		return []BranchInfo{}, nil //nolint:nilerr // no indexes yet
	}
	var out []BranchInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, ok := readBranchMeta(r.indexHome(e.Name()))
		if !ok {
			continue
		}
		bi := BranchInfo{Name: e.Name(), Parent: meta.Parent, CreatedAt: meta.CreatedAt, LastAccessedAt: meta.LastAccessedAt}
		if st, err := r.Get(e.Name()); err == nil {
			if s, err := st.IndexStatus(); err == nil {
				bi.Documents = s.Documents // local diffs only
			}
		}
		out = append(out, bi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
