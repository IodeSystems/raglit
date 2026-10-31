package raglit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Index access control.
//
// The grain is the INDEX, and a branch is an index. Per-document visibility was
// rejected: every read path — FTS, vectors, figures, documents, page images,
// readings, attest — would need its own filter, and the one that is missed is a
// leak nobody sees. Per index, there is one gate: the lookup.
//
// Off unless the daemon runs with OIDC (EnableACL). Off, Read and Write are Get,
// which is exactly today's behavior.
//
// Rules:
//   - read(i)  = (public(i) ∨ owner(i) = p) ∧ read(parent(i))
//   - write(i) = owner(i) = p — a public index is shared for READING; others
//     fork a branch to add to it.
//   - An index with no acl.json belongs to the configured admin, private. A
//     shared daemon opens nothing up by default.
//   - An unreadable index is "no such index", never "forbidden": telling a caller
//     that a private index exists is itself a disclosure.

// Visibility says who may read an index.
type Visibility string

const (
	Private Visibility = "private"
	Public  Visibility = "public"
)

// ACL is an index's owner and visibility, stored at <index home>/acl.json.
type ACL struct {
	Owner      string     `json:"owner"`
	Visibility Visibility `json:"visibility"`
}

// ErrNoIndex is what an unreadable index looks like from outside.
var ErrNoIndex = errors.New("raglit: no such index")

// ErrForbidden is a write refused on an index the caller can read.
var ErrForbidden = errors.New("raglit: only the index owner may change it")

type principalKey struct{}

// WithPrincipal records the authenticated account on a request context.
func WithPrincipal(ctx context.Context, p string) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the authenticated account, "" when there is none.
func PrincipalFrom(ctx context.Context) string {
	p, _ := ctx.Value(principalKey{}).(string)
	return p
}

func aclPath(home Home) string { return filepath.Join(string(home), "acl.json") }

func readACL(home Home) (ACL, bool) {
	b, err := os.ReadFile(aclPath(home))
	if err != nil {
		return ACL{}, false
	}
	var a ACL
	if json.Unmarshal(b, &a) != nil || a.Owner == "" {
		return ACL{}, false
	}
	return a, true
}

func writeACL(home Home, a ACL) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := aclPath(home) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, aclPath(home))
}

// EnableACL turns on per-index access control. admin owns every index that has
// no acl.json. Scoped registries only: single-home indexes share one directory,
// so there is nowhere to put a per-index ACL.
func (r *Registry) EnableACL(admin string) error {
	if r.scopedRoot == "" {
		return fmt.Errorf("raglit: access control requires the daemon's scoped storage (no --home)")
	}
	if admin == "" {
		return fmt.Errorf("raglit: access control needs an admin principal to own existing indexes")
	}
	r.mu.Lock()
	r.admin = admin
	r.mu.Unlock()
	return nil
}

// ACLEnabled reports whether access control is on.
func (r *Registry) ACLEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.admin != ""
}

// ACLOf returns an index's ACL, defaulting to admin-owned and private.
func (r *Registry) ACLOf(name string) ACL {
	name = normalizeIndexName(name)
	if a, ok := readACL(r.indexHome(name)); ok {
		return a
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return ACL{Owner: r.admin, Visibility: Private}
}

// cachedOrExists reports whether name is an open store or has storage.
func (r *Registry) cachedOrExists(name string) bool {
	name = normalizeIndexName(name)
	r.mu.Lock()
	_, ok := r.stores[name]
	r.mu.Unlock()
	return ok || r.exists(name)
}

func (r *Registry) exists(name string) bool {
	_, err := os.Stat(string(r.indexHome(name)))
	return err == nil
}

// CanRead applies read(i) to the caller on ctx. Always true with access control
// off.
func (r *Registry) CanRead(ctx context.Context, name string) bool {
	if !r.ACLEnabled() {
		return true
	}
	p := PrincipalFrom(ctx)
	if p == "" {
		return false
	}
	name = normalizeIndexName(name)
	// Depth-bounded: branch.json is a file on disk, and a cycle in it must not
	// hang a request.
	for range 64 {
		a := r.ACLOf(name)
		if a.Visibility != Public && a.Owner != p {
			return false
		}
		meta, ok := readBranchMeta(r.indexHome(name))
		if !ok {
			return true
		}
		name = normalizeIndexName(meta.Parent)
	}
	return false
}

// Read opens an index the caller may read. An index the caller may not read,
// or that does not exist, is ErrNoIndex — and is never created, with access
// control on or off: a read of a wrong name used to leave an empty index
// behind, listed everywhere, which made the next choice of name harder.
func (r *Registry) Read(ctx context.Context, name string) (*Store, error) {
	if parent, ok := SessionBranchParent(name); ok && r.Scoped() && !r.cachedOrExists(name) {
		name = parent // an unwritten session branch reads as its parent
	}
	if !r.ACLEnabled() {
		st, err := r.Existing(name)
		if err != nil {
			return nil, ErrNoIndex
		}
		return st, nil
	}
	name = normalizeIndexName(name)
	if !r.exists(name) || !r.CanRead(ctx, name) {
		return nil, ErrNoIndex
	}
	return r.Existing(name)
}

// Write opens an index the caller owns. A new name is created and owned by the
// caller, private. An index the caller can read but does not own is
// ErrForbidden; one it cannot read is ErrNoIndex.
func (r *Registry) Write(ctx context.Context, name string) (*Store, error) {
	if !r.ACLEnabled() {
		return r.Get(name)
	}
	p := PrincipalFrom(ctx)
	if p == "" {
		return nil, ErrNoIndex
	}
	name = normalizeIndexName(name)
	if !r.exists(name) {
		// A session branch exists from its first write: forked from its
		// parent, owned by the writer, private. Nothing creates it earlier.
		if parent, ok := SessionBranchParent(name); ok {
			// A parent nothing has written (a sessions index: sessions never
			// write main) is created empty, admin-owned and PUBLIC — main is
			// public, and a branch is readable only where its parent is.
			if !r.exists(parent) {
				if _, err := r.Get(parent); err != nil {
					return nil, err
				}
				if err := writeACL(r.indexHome(parent), ACL{Owner: r.Admin(), Visibility: Public}); err != nil {
					return nil, err
				}
			}
			if err := r.ForkBranchAs(ctx, name, parent); err != nil {
				return nil, err
			}
			return r.Existing(name)
		}
		st, err := r.Get(name)
		if err != nil {
			return nil, err
		}
		if err := writeACL(r.indexHome(name), ACL{Owner: p, Visibility: Private}); err != nil {
			return nil, err
		}
		return st, nil
	}
	if !r.CanRead(ctx, name) {
		return nil, ErrNoIndex
	}
	if r.ACLOf(name).Owner != p {
		return nil, ErrForbidden
	}
	return r.Existing(name)
}

// Readable filters names to the ones the caller may read, keeping order.
func (r *Registry) Readable(ctx context.Context, names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if r.Scoped() && !r.cachedOrExists(n) {
			// A session branch nobody has written yet is, by overlay, its
			// parent: reading it reads the parent.
			parent, ok := SessionBranchParent(n)
			if !ok || !r.cachedOrExists(parent) {
				continue // not an index: nothing to read, and nothing to create
			}
			n = parent
		}
		if !r.ACLEnabled() || r.CanRead(ctx, n) {
			out = append(out, n)
		}
	}
	return out
}

// SetVisibility changes who may read an index. Owner only.
func (r *Registry) SetVisibility(ctx context.Context, name string, v Visibility) error {
	if v != Private && v != Public {
		return fmt.Errorf("raglit: visibility must be %q or %q", Private, Public)
	}
	if !r.ACLEnabled() {
		return fmt.Errorf("raglit: access control is off; every index is readable")
	}
	if _, err := r.Write(ctx, name); err != nil {
		return err
	}
	name = normalizeIndexName(name)
	a := r.ACLOf(name)
	a.Visibility = v
	return writeACL(r.indexHome(name), a)
}

// ForkBranchAs forks a branch the caller owns, private. The caller must be able
// to read the parent: a fork is a read of everything in it.
func (r *Registry) ForkBranchAs(ctx context.Context, name, parent string) error {
	if !r.ACLEnabled() {
		return r.ForkBranch(name, parent)
	}
	p := PrincipalFrom(ctx)
	parent = normalizeIndexName(parent)
	if p == "" || !r.exists(parent) || !r.CanRead(ctx, parent) {
		return fmt.Errorf("raglit: parent %q: %w", parent, ErrNoIndex)
	}
	name = normalizeIndexName(name)
	if r.exists(name) {
		// Existing name: report "taken" only to a caller that may see it.
		if r.CanRead(ctx, name) {
			return fmt.Errorf("raglit: index %q already exists", name)
		}
		return fmt.Errorf("raglit: index %q is not available", name)
	}
	if err := r.ForkBranch(name, parent); err != nil {
		return err
	}
	return writeACL(r.indexHome(name), ACL{Owner: p, Visibility: Private})
}

// DeleteAs removes an index or branch the caller owns.
func (r *Registry) DeleteAs(ctx context.Context, name string, branch bool) error {
	if _, err := r.writeExisting(ctx, name); err != nil {
		return err
	}
	if branch {
		return r.DeleteBranch(name)
	}
	return r.DeleteIndex(name)
}

// writeExisting is Write without creating: deleting a name that is not there
// must not make it first.
func (r *Registry) writeExisting(ctx context.Context, name string) (*Store, error) {
	if !r.ACLEnabled() {
		return nil, nil
	}
	if !r.exists(normalizeIndexName(name)) {
		return nil, ErrNoIndex
	}
	return r.Write(ctx, name)
}

// Admin is the principal that owns indexes without an acl.json; "" when access
// control is off.
func (r *Registry) Admin() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.admin
}

// TransferOwnership hands an index to another principal — how a maintainer
// service takes over a project's base. The current owner or the admin may do
// it; visibility is unchanged. Readers of a branch are unaffected; writers are
// now the new owner only.
func (r *Registry) TransferOwnership(ctx context.Context, name, to string) error {
	if !r.ACLEnabled() {
		return fmt.Errorf("raglit: access control is off; indexes have no owners")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("raglit: the new owner is required")
	}
	p := PrincipalFrom(ctx)
	name = normalizeIndexName(name)
	admin := p != "" && p == r.Admin()
	// The admin may move any index, private ones included: that is how one is
	// recovered from an owner who has left.
	if p == "" || !r.exists(name) || (!admin && !r.CanRead(ctx, name)) {
		return ErrNoIndex
	}
	a := r.ACLOf(name)
	if a.Owner != p && !admin {
		return ErrForbidden
	}
	a.Owner = to
	return writeACL(r.indexHome(name), a)
}
