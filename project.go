package raglit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Shared projects: one BASE index per project id, one LAYER (a P6 branch) per
// person over it.
//
// A client turning on rag for a repo registers under the repo's project id (dun:
// the root commit, labelled with the normalized origin; raglit treats the id as
// opaque text). The first registration creates the base, PUBLIC and empty, and
// every registrant — the first included — writes to their own private layer
// over it. USER decision 2026-10-19: the base is not the first person's
// memories; it is what a maintainer folds the public layers into.
//
// Why the base is public: a layer is readable only when its parent is (acl.go),
// so a private base would make every other person's layer unreadable — even to
// them.
//
// A layer marked public is searched by every other layer on the same base
// (PublicSiblings). That is how a note becomes shared without being merged.

// ProjectMeta marks an index as a project's base: <home>/project.json.
type ProjectMeta struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// Registration answers RegisterProject: the base, and the caller's layer, which
// is where the caller writes.
type Registration struct {
	ProjectID string `json:"project_id"`
	Base      string `json:"base"`
	Layer     string `json:"layer"`
	NewBase   bool   `json:"new_base"`
	NewLayer  bool   `json:"new_layer"`
}

func projectPath(home Home) string { return filepath.Join(string(home), "project.json") }

func readProjectMeta(home Home) (ProjectMeta, bool) {
	b, err := os.ReadFile(projectPath(home))
	if err != nil {
		return ProjectMeta{}, false
	}
	var m ProjectMeta
	if json.Unmarshal(b, &m) != nil || m.ID == "" {
		return ProjectMeta{}, false
	}
	return m, true
}

func writeProjectMeta(home Home, m ProjectMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(projectPath(home), b, 0o644)
}

// indexDirs lists the scoped registry's index directory names.
func (r *Registry) indexDirs() []string {
	entries, err := os.ReadDir(filepath.Join(r.scopedRoot, "indexes"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// ProjectBase returns the base index registered for a project id, "" if none.
func (r *Registry) ProjectBase(id string) string {
	for _, n := range r.indexDirs() {
		if m, ok := readProjectMeta(r.indexHome(n)); ok && m.ID == id {
			return n
		}
	}
	return ""
}

// LayersOf lists the branches whose parent is base.
func (r *Registry) LayersOf(base string) []string {
	var out []string
	for _, n := range r.indexDirs() {
		if meta, ok := readBranchMeta(r.indexHome(n)); ok && normalizeIndexName(meta.Parent) == base {
			out = append(out, n)
		}
	}
	return out
}

// baseName derives a base index name from a project id: readable prefix plus a
// hash, so two ids that sanitize alike do not collide.
func baseName(id string) string {
	sum := sha256.Sum256([]byte(id))
	stem := normalizeIndexName(id)
	if len(stem) > 16 {
		stem = stem[:16]
	}
	return "proj-" + stem + "-" + hex.EncodeToString(sum[:4])
}

// layerName is the caller's layer over base.
func layerName(base, principal string) string {
	sum := sha256.Sum256([]byte(principal))
	return base + "-l" + hex.EncodeToString(sum[:4])
}

// RegisterProject is register-or-layer, atomic per registry: the base for id
// (created public and empty when absent) and the caller's private layer over it
// (created when absent). Repeating a registration returns the same pair.
func (r *Registry) RegisterProject(ctx context.Context, id, label string) (Registration, error) {
	if r.scopedRoot == "" {
		return Registration{}, fmt.Errorf("raglit: projects require the daemon's scoped storage (no --home)")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Registration{}, fmt.Errorf("raglit: project id is required")
	}
	p := PrincipalFrom(ctx)
	if r.ACLEnabled() && p == "" {
		return Registration{}, ErrNoIndex
	}
	// One registration at a time: two first-time registrants racing must end on
	// ONE base, not two.
	r.registerMu.Lock()
	defer r.registerMu.Unlock()

	reg := Registration{ProjectID: id, Base: r.ProjectBase(id)}
	if reg.Base == "" {
		reg.Base = baseName(id)
		if r.exists(reg.Base) {
			return Registration{}, fmt.Errorf("raglit: index %q exists and is not project %q's base", reg.Base, id)
		}
		if _, err := r.Write(ctx, reg.Base); err != nil {
			return Registration{}, err
		}
		if r.ACLEnabled() {
			if err := r.SetVisibility(ctx, reg.Base, Public); err != nil {
				return Registration{}, err
			}
		}
		if err := writeProjectMeta(r.indexHome(reg.Base), ProjectMeta{ID: id, Label: label, CreatedAt: time.Now().Unix()}); err != nil {
			return Registration{}, err
		}
		reg.NewBase = true
	}

	reg.Layer = layerName(reg.Base, p)
	if r.exists(reg.Layer) {
		if r.ACLEnabled() && r.ACLOf(reg.Layer).Owner != p {
			return Registration{}, fmt.Errorf("raglit: layer %q belongs to someone else", reg.Layer)
		}
		return reg, nil
	}
	if err := r.ForkBranchAs(ctx, reg.Layer, reg.Base); err != nil {
		return Registration{}, err
	}
	reg.NewLayer = true
	return reg, nil
}

// PublicSiblings lists the layers that share name's parent, are public, and the
// caller can read — what a search on name fans out to besides name itself.
// Empty for an index that is not a branch.
func (r *Registry) PublicSiblings(ctx context.Context, name string) []string {
	if r.scopedRoot == "" || !r.ACLEnabled() {
		return nil
	}
	name = normalizeIndexName(name)
	meta, ok := readBranchMeta(r.indexHome(name))
	if !ok {
		return nil
	}
	var out []string
	for _, sib := range r.LayersOf(normalizeIndexName(meta.Parent)) {
		if sib == name || r.ACLOf(sib).Visibility != Public || !r.CanRead(ctx, sib) {
			continue
		}
		out = append(out, sib)
	}
	return out
}

// SearchOwnLayer searches this layer's OWN documents only — not its parent.
// For sibling fan-out: the caller's layer already overlays the shared parent,
// so searching a sibling's overlay again would return the parent twice.
// mode is "" / "bm25", "vec" or "hybrid".
func (s *Store) SearchOwnLayer(ctx context.Context, query, mode, pathPrefix string, limit int) ([]Hit, error) {
	lex := func(n int) ([]Hit, error) {
		hits, err := s.searchLocal(query, pathPrefix, n*2)
		if err != nil {
			return nil, err
		}
		return s.collapseReadings(hits, n), nil
	}
	vec := func(n int) ([]Hit, error) {
		if s.embedder == nil {
			return nil, fmt.Errorf("raglit: vector search needs an embedder (SetEmbedder)")
		}
		qv, err := s.embedder.EmbedQuery(ctx, query)
		if err != nil {
			return nil, err
		}
		return s.vecSearchLocal(qv, query, pathPrefix, n)
	}
	switch mode {
	case "vec":
		return vec(limit)
	case "hybrid":
		l, err := lex(limit * 4)
		if err != nil {
			return nil, err
		}
		v, err := vec(limit * 4)
		if err != nil {
			return nil, err
		}
		return fuseRRF(l, v, limit), nil
	default:
		return lex(limit)
	}
}
