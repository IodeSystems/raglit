package raglit

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree branches (docs/design/ragcore.md R11).
//
// A git worktree holds the same files as its main checkout at different
// absolute paths. A branch opened with a root (OpenBranch) translates paths
// where it reads through to its parent: a branch path under root is looked up
// in the parent as parentRoot + rel, and a parent path under parentRoot comes
// back as root + rel. Shadowing and tombstones key on branch paths. A
// main-checkout path (under parentRoot, not under root) is not a path the
// branch shows; a path outside both roots passes through untranslated.
//
// A branch with no root (a Registry or session branch) does no translation and
// does not read DocumentHash or Tags through to its parent: its behavior is the
// same as before roots existed.

// ErrParentOnly is the error (errors.Is) a branch returns for a write to a
// document that only its parent holds. A branch cannot change a parent's
// document; ingest the document into the branch first.
var ErrParentOnly = errors.New("raglit: the document is in the parent index only; a branch cannot change a parent's document")

// worktree reports whether this store is a branch that translates paths.
func (s *Store) worktree() bool { return s.parent != nil && s.root != "" }

// splitScheme separates a "file://" prefix from a document path. Document
// paths are stored as given: a bare absolute path or a file:// URL.
func splitScheme(p string) (scheme, rest string) {
	if r, ok := strings.CutPrefix(p, "file://"); ok {
		return "file://", r
	}
	return "", p
}

// rebase moves p from under dir from to under dir to, keeping its scheme. A
// path not under from is returned unchanged with false.
func rebase(p, from, to string) (string, bool) {
	scheme, rest := splitScheme(p)
	if rest == from {
		return scheme + to, true
	}
	if strings.HasPrefix(rest, from+"/") {
		return scheme + to + rest[len(from):], true
	}
	return p, false
}

// under reports whether p, with any scheme removed, is dir or inside it.
func under(p, dir string) bool {
	_, rest := splitScheme(p)
	return rest == dir || strings.HasPrefix(rest, dir+"/")
}

// toParent is branch path p as the parent stores it, and false when the branch
// does not show p. A path under the parent root and not under the root names a
// main-checkout file: a worktree branch never shows one (listings and
// MatchDocuments leave them out), so a point lookup must not reach the
// parent's copy either. A path outside both roots passes through.
func (s *Store) toParent(p string) (string, bool) {
	if !s.worktree() {
		return p, true
	}
	if q, ok := rebase(p, s.root, s.parentRoot); ok {
		return q, true
	}
	if under(p, s.parentRoot) {
		return "", false
	}
	return p, true
}

// fromParent is parent path p as this branch shows it, and false when the
// branch does not show it. A parent path under the root is left out: when the
// root is inside the parent root (<main>/.worktrees/x), it is the main
// checkout's index of the worktree's own files, and rebasing it again would
// name a file that does not exist.
func (s *Store) fromParent(p string) (string, bool) {
	if !s.worktree() {
		return p, true
	}
	if under(p, s.root) {
		return "", false
	}
	q, _ := rebase(p, s.parentRoot, s.root)
	return q, true
}

// parentPrefixes are the path prefixes to ask the parent for, so that its
// answers cover every document the branch-path prefix admits. Each answer is
// then translated and checked against the branch prefix (admitParent).
//
// A prefix inside the root becomes the same prefix inside the parent root. A
// prefix that covers the whole root (a directory above it, or "") also needs
// the parent root itself, because the parent's documents there have paths the
// prefix does not cover.
func (s *Store) parentPrefixes(prefix string) []string {
	if !s.worktree() || prefix == "" {
		return []string{prefix}
	}
	scheme, rest := splitScheme(prefix)
	if q, ok := rebase(prefix, s.root, s.parentRoot); ok {
		return []string{q}
	}
	if strings.HasPrefix(s.root, rest) {
		return []string{prefix, scheme + s.parentRoot + "/"}
	}
	return []string{prefix}
}

// admitParent reports whether a translated parent path bp is inside the
// branch-path prefix. Always true for a branch that does not translate: the
// parent applied the same prefix.
func (s *Store) admitParent(bp, prefix string) bool {
	return !s.worktree() || prefix == "" || strings.HasPrefix(bp, prefix)
}

// tombstoned reports whether this layer tombstoned path.
func (s *Store) tombstoned(path string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tombstones WHERE path = ?`, path).Scan(&n)
	return n > 0, err
}

// rowQuerier is a *sql.DB or a *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// parentOnly reports whether path, which this layer has no row for, is a
// document this branch shows only from its parent: not tombstoned here and
// shown by the parent. q reads this layer (pass the open tx, if any: an
// in-memory store has one connection).
func (s *Store) parentOnly(ctx context.Context, q rowQuerier, path string) (bool, error) {
	if s.parent == nil {
		return false, nil
	}
	var tomb int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tombstones WHERE path = ?`, path).Scan(&tomb); err != nil {
		return false, err
	}
	if tomb > 0 {
		return false, nil
	}
	pp, ok := s.toParent(path)
	if !ok {
		return false, nil
	}
	_, shown := s.parent.viewHash(pp)
	return shown, nil
}

// noDocumentErr is the error for a write to a path this layer has no document
// for: ErrParentOnly when the parent shows it, else "no document".
func (s *Store) noDocumentErr(ctx context.Context, op, path string) error {
	only, err := s.parentOnly(ctx, s.db, path)
	if err != nil {
		return err
	}
	if only {
		return fmt.Errorf("raglit: %s: %q: %w", op, path, ErrParentOnly)
	}
	return fmt.Errorf("raglit: no document with path %q", path)
}

// overlayHits appends the parent's hits for f to hits: translated to branch
// paths, inside the branch-path prefix, not shadowed, and each (path, fragment)
// once. fetch runs one parent query for a filter.
func (s *Store) overlayHits(hits []Hit, f SearchFilter, shadow map[string]bool, fetch func(SearchFilter) ([]Hit, error)) ([]Hit, error) {
	type key struct {
		path string
		id   int64
	}
	seen := map[key]bool{}
	for _, pp := range s.parentPrefixes(f.PathPrefix) {
		pf := f
		pf.PathPrefix = pp
		phits, err := fetch(pf)
		if err != nil {
			return nil, err
		}
		for _, ph := range phits {
			bp, ok := s.fromParent(ph.Path)
			if !ok || shadow[bp] || !s.admitParent(bp, f.PathPrefix) {
				continue
			}
			if s.worktree() {
				k := key{bp, ph.ID}
				if seen[k] {
					continue
				}
				seen[k] = true
			}
			ph.Path = bp
			hits = append(hits, ph)
		}
	}
	return hits, nil
}

// cleanRoot returns an absolute root directory, cleaned, or an error.
func cleanRoot(name, r string) (string, error) {
	if !filepath.IsAbs(r) {
		return "", fmt.Errorf("raglit: open branch: %s %q is not an absolute directory", name, r)
	}
	r = filepath.Clean(r)
	if r == string(filepath.Separator) {
		return "", fmt.Errorf("raglit: open branch: %s cannot be the filesystem root", name)
	}
	return r, nil
}

// OpenBranch opens the branch at home branch over the index at home parent,
// creating the branch home and its branch.json when they do not exist. It is
// idempotent: a second call opens the same branch. It returns an error when an
// existing branch.json names a different parent, root or parent root, or when
// branch holds an index that is not a branch.
//
// root and parentRoot are absolute directories: the worktree's and the main
// checkout's. With both set, the branch translates paths at the overlay
// boundary (see the top of worktree.go), so its index stores only the files
// that differ from the parent's, plus tombstones. With both empty, the branch
// does no translation. One set and the other empty is an error.
//
// Translation is a string prefix match: OpenBranch does not resolve symlinks.
// root must be spelled as the paths the branch's documents are ingested under,
// and parentRoot as the paths the parent index stores. A different spelling
// (a symlinked directory, or git's resolved path from WorktreeOf against an
// index of unresolved paths) translates nothing and reports nothing: every
// worktree file counts as new, and the parent's documents show at their
// main-checkout paths. root may be inside parentRoot.
//
// The parent index must exist. OpenBranch opens its own handle on it and does
// not wire the parent's own parent. The returned store owns that handle: its
// Close closes the parent too. Two branches of one parent each hold a handle.
func OpenBranch(parent Home, branch Home, root, parentRoot string) (*Store, error) {
	return openBranch(parent, nil, branch, root, parentRoot)
}

// OpenBranchOn is OpenBranch over parent, an index the caller has open
// already, instead of a handle of its own. parent must be the default index
// of its home (OpenHome); branch.json records that home, as OpenBranch does.
// The returned store does not own parent: its Close leaves parent open, and
// the caller must keep parent open until the branch is closed.
func OpenBranchOn(parent *Store, branch Home, root, parentRoot string) (*Store, error) {
	if parent == nil || !parent.withHome || parent.path != parent.home.IndexPath() {
		return nil, fmt.Errorf("raglit: open branch: the parent must be a home's default index (OpenHome)")
	}
	return openBranch(parent.home, parent, branch, root, parentRoot)
}

// openBranch is OpenBranch and OpenBranchOn. With ps nil it opens its own
// handle on parent and the branch owns it; otherwise ps is the parent.
func openBranch(parent Home, ps *Store, branch Home, root, parentRoot string) (*Store, error) {
	var err error
	if (root == "") != (parentRoot == "") {
		return nil, fmt.Errorf("raglit: open branch: root and parent root must both be set or both be empty")
	}
	if root != "" {
		if root, err = cleanRoot("root", root); err != nil {
			return nil, err
		}
		if parentRoot, err = cleanRoot("parent root", parentRoot); err != nil {
			return nil, err
		}
	}
	pabs, err := filepath.Abs(string(parent))
	if err != nil {
		return nil, err
	}
	babs, err := filepath.Abs(string(branch))
	if err != nil {
		return nil, err
	}
	if pabs == babs {
		return nil, fmt.Errorf("raglit: open branch: a branch cannot be its own parent")
	}
	if _, err := os.Stat(Home(pabs).IndexPath()); err != nil {
		return nil, fmt.Errorf("raglit: open branch: parent index: %w", err)
	}
	if meta, ok := readBranchMeta(branch); ok {
		mp := meta.Parent
		if a, err := filepath.Abs(mp); err == nil {
			mp = a
		}
		if mp != pabs {
			return nil, fmt.Errorf("raglit: open branch: %s is a branch of %s, not of %s", babs, meta.Parent, pabs)
		}
		if meta.Root != root || meta.ParentRoot != parentRoot {
			return nil, fmt.Errorf("raglit: open branch: %s has root %q over %q, not %q over %q",
				babs, meta.Root, meta.ParentRoot, root, parentRoot)
		}
	} else {
		if fi, err := os.Stat(branch.IndexPath()); err == nil && fi.Size() > 0 {
			return nil, fmt.Errorf("raglit: open branch: %s holds an index that is not a branch", babs)
		}
		if err := branch.Ensure(); err != nil {
			return nil, err
		}
		now := time.Now().UnixNano()
		if err := writeBranchMeta(branch, BranchMeta{Parent: pabs, Root: root, ParentRoot: parentRoot,
			CreatedAt: now, LastAccessedAt: now}); err != nil {
			return nil, err
		}
	}
	p, owned := ps, false
	if p == nil {
		if p, err = OpenHome(Home(pabs)); err != nil {
			return nil, err
		}
		owned = true
	}
	s, err := OpenHome(branch)
	if err != nil {
		if owned {
			_ = p.Close()
		}
		return nil, err
	}
	s.SetParent(p)
	s.root, s.parentRoot = root, parentRoot
	if owned {
		s.ownedParent = p
	}
	return s, nil
}

// GCBranches removes each branch home whose branch.json names a root for which
// alive returns false, and returns how many it removed. A home with no
// branch.json, or a branch with no root (a session branch), is never removed.
// The caller lists its branch homes and must not have them open.
func GCBranches(branches []Home, alive func(root string) bool) (removed int, err error) {
	var errs []error
	for _, h := range branches {
		meta, ok := readBranchMeta(h)
		if !ok || meta.Root == "" || alive(meta.Root) {
			continue
		}
		if e := os.RemoveAll(string(h)); e != nil {
			errs = append(errs, e)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// WorktreeOf returns the work tree of the main checkout when dir is inside a
// linked git worktree. ok is false for a main checkout, a bare repository's
// worktree and a directory outside git. It asks git (rev-parse
// --git-common-dir) and, when git cannot run, reads the .git file's gitdir
// pointer. It only answers the question; the caller decides which index is the
// parent.
//
// mainRoot is spelled as git records it, which is usually with symlinks
// resolved, and may differ from the spelling of dir and of the paths an index
// holds. Before passing it to OpenBranch, spell it as the parent index's
// document paths are spelled (see OpenBranch).
func WorktreeOf(dir string) (mainRoot string, ok bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	cmd := exec.Command("git", "rev-parse", "--git-dir", "--git-common-dir")
	cmd.Dir = abs
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", false // git ran: not a repository
		}
		return worktreeFromGitFile(abs)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", false
	}
	resolve := func(p string) string {
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(abs, p)
		}
		return filepath.Clean(p)
	}
	return mainOf(resolve(lines[0]), resolve(lines[1]))
}

// mainOf is the main work tree for a git dir and its common dir: the common
// dir's parent when the common dir is a ".git" directory and differs from the
// git dir.
func mainOf(gitDir, commonDir string) (string, bool) {
	if gitDir == commonDir || filepath.Base(commonDir) != ".git" {
		return "", false
	}
	return filepath.Dir(commonDir), true
}

// worktreeFromGitFile finds the nearest .git at or above dir. A linked
// worktree's .git is a file "gitdir: <dir>", and that dir's commondir file
// names the common dir, relative to it.
func worktreeFromGitFile(dir string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		gp := filepath.Join(d, ".git")
		fi, err := os.Stat(gp)
		if err == nil {
			if fi.IsDir() {
				return "", false // a main checkout
			}
			return readGitFile(gp)
		}
		if filepath.Dir(d) == d {
			return "", false
		}
	}
}

func readGitFile(gp string) (string, bool) {
	f, err := os.Open(gp)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return "", false
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "gitdir:")
	if !ok {
		return "", false
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(gp), gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	common := strings.TrimSpace(string(b))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return mainOf(gitDir, filepath.Clean(common))
}
