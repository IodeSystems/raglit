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

// Shared projects: one BASE index per project id, and per person TWO layers (P6
// branches) over it — one private, one public.
//
// A client turning on rag for a repo registers under the repo's project id (dun:
// the root commit, labelled with the normalized origin; raglit treats the id as
// opaque text). The first registration creates the base, PUBLIC and empty, and
// every registrant — the first included — writes to their own layers over it.
// USER decision 2026-10-19: the base is not the first person's memories; it is
// what a maintainer folds the public layers into.
//
// Why the base is public: a layer is readable only when its parent is (acl.go),
// so a private base would make every other person's layer unreadable — even to
// them.
//
// Two layers because visibility is per index and public:true is per NOTE: with
// one layer, publishing one note would publish every private note beside it
// (USER, 2026-10-19). The client writes each note to the layer it belongs in.
//
// Every name sits in one namespace — <ns>__base, <ns>__u<hash>-priv|-pub — so a
// client configured with project = <ns> reaches the whole project through the
// daemon MCP's ordinary "<ns>__*" selector.
//
// Sharing between layers is not a project feature: search on any branch fans
// out to its readable siblings (Registry.Siblings). Ownership is the ACL on top.

// ProjectMeta marks an index as a project's base: <home>/project.json.
type ProjectMeta struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// Registration answers RegisterProject.
type Registration struct {
	ProjectID string `json:"project_id"`
	// Namespace prefixes every name below; it is the client's project setting
	// for the daemon MCP.
	Namespace string `json:"namespace"`
	Base      string `json:"base"`
	// Private and Public are the caller's two layers — where the caller writes.
	Private    string `json:"private"`
	Public     string `json:"public"`
	NewBase    bool   `json:"new_base"`
	NewPrivate bool   `json:"new_private"`
	NewPublic  bool   `json:"new_public"`
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
	base = normalizeIndexName(base)
	var out []string
	for _, n := range r.indexDirs() {
		if meta, ok := readBranchMeta(r.indexHome(n)); ok && normalizeIndexName(meta.Parent) == base {
			out = append(out, n)
		}
	}
	return out
}

// ParentOf is a branch's parent, "" for an index that is not a branch.
func (r *Registry) ParentOf(name string) string {
	if r.scopedRoot == "" {
		return ""
	}
	if meta, ok := readBranchMeta(r.indexHome(normalizeIndexName(name))); ok {
		return normalizeIndexName(meta.Parent)
	}
	return ""
}

// Siblings lists the branches that share name's parent and that the caller may
// read — what a search on name fans out to besides name itself. Empty for an
// index that is not a branch. With access control off every sibling is
// readable.
func (r *Registry) Siblings(ctx context.Context, name string) []string {
	name = normalizeIndexName(name)
	parent := r.ParentOf(name)
	if parent == "" {
		return nil
	}
	var out []string
	for _, sib := range r.LayersOf(parent) {
		if sib != name && r.CanRead(ctx, sib) {
			out = append(out, sib)
		}
	}
	return out
}

// projectNamespace derives a namespace from a project id: readable prefix plus a
// hash, so two ids that sanitize alike do not collide. No underscores, because
// "__" separates a namespace from the index name.
func projectNamespace(id string) string {
	sum := sha256.Sum256([]byte(id))
	stem := strings.ReplaceAll(normalizeIndexName(id), "_", "")
	if len(stem) > 16 {
		stem = stem[:16]
	}
	return "p" + stem + hex.EncodeToString(sum[:4])
}

// layerNames are the caller's private and public layers in namespace ns.
func layerNames(ns, principal string) (priv, pub string) {
	sum := sha256.Sum256([]byte(principal))
	u := ns + "__u" + hex.EncodeToString(sum[:4])
	return u + "-priv", u + "-pub"
}

// RegisterProject is register-or-layer, atomic per registry: the base for id
// (created public and empty when absent) and the caller's private and public
// layers over it (each created when absent). Repeating a registration returns
// the same names.
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
		reg.Base = projectNamespace(id) + "__base"
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
	reg.Namespace, _, _ = strings.Cut(reg.Base, "__")
	reg.Private, reg.Public = layerNames(reg.Namespace, p)

	var err error
	if reg.NewPrivate, err = r.ensureLayer(ctx, reg.Private, reg.Base, Private); err != nil {
		return Registration{}, err
	}
	if reg.NewPublic, err = r.ensureLayer(ctx, reg.Public, reg.Base, Public); err != nil {
		return Registration{}, err
	}
	return reg, nil
}

// ensureLayer forks the caller's layer over base when it is absent. An existing
// layer must be the caller's.
func (r *Registry) ensureLayer(ctx context.Context, name, base string, v Visibility) (bool, error) {
	if r.exists(name) {
		if r.ACLEnabled() && r.ACLOf(name).Owner != PrincipalFrom(ctx) {
			return false, fmt.Errorf("raglit: layer %q belongs to someone else", name)
		}
		return false, nil
	}
	if err := r.ForkBranchAs(ctx, name, base); err != nil {
		return false, err
	}
	if v == Public && r.ACLEnabled() {
		if err := r.SetVisibility(ctx, name, Public); err != nil {
			return false, err
		}
	}
	return true, nil
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

// ProjectIDOf is the project id a base index was registered under, "" when it
// is not a project base.
func (r *Registry) ProjectIDOf(base string) string {
	if r.scopedRoot == "" {
		return ""
	}
	if m, ok := readProjectMeta(r.indexHome(normalizeIndexName(base))); ok {
		return m.ID
	}
	return ""
}
