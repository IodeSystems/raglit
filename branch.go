package raglit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Branch storage.
//
// A branch Store (parent != nil) overlays branch-over-parent at DOCUMENT grain:
// a doc written to the branch, or tombstoned in it, shadows the parent's; every
// other parent doc shows through. Writes go to the branch only — the parent is
// untouched (copy-on-write). The branch stores just its diffs (changed/added
// docs + tombstones), so forking is cheap and a branch reflects only what it
// changed. NOT yet overlaid: VecSearch and DocReview (they see the branch layer
// only) and merge/diff operations — see plan/daemon-stack.md P6.

// shadowedPaths is the set of parent doc paths hidden by this branch: docs the
// branch has its own copy of, plus tombstoned (deleted-in-branch) paths.
func (s *Store) shadowedPaths() (map[string]bool, error) {
	ctx := context.Background()
	m := map[string]bool{}
	paths, err := s.q.ListDocumentPaths(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		m[p] = true
	}
	ts, err := s.q.ListTombstones(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range ts {
		m[p] = true
	}
	return m, nil
}

// Search overlays branch-over-parent: branch hits, then parent hits whose doc is
// not shadowed, re-ranked by score and truncated to limit. A non-branch store
// (parent == nil) is just its local FTS.
func (s *Store) Search(query string, limit int) ([]Hit, error) {
	hits, err := s.searchLocal(query, limit)
	if err != nil || s.parent == nil {
		return hits, err
	}
	s.touchAccess()
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	phits, err := s.parent.Search(query, limit)
	if err != nil {
		return nil, err
	}
	for _, ph := range phits {
		if !shadow[ph.Path] {
			hits = append(hits, ph)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// Documents overlays: branch docs, then parent docs not shadowed.
func (s *Store) Documents() ([]DocSummary, error) {
	docs, err := s.documentsLocal()
	if err != nil || s.parent == nil {
		return docs, err
	}
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	pdocs, err := s.parent.Documents()
	if err != nil {
		return nil, err
	}
	for _, pd := range pdocs {
		if !shadow[pd.Path] {
			docs = append(docs, pd)
		}
	}
	return docs, nil
}

// MatchDocuments overlays: branch matches, then parent matches not shadowed.
func (s *Store) MatchDocuments(ref string) ([]DocRef, error) {
	local, err := s.matchDocumentsLocal(ref)
	if err != nil || s.parent == nil {
		return local, err
	}
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	pmatch, err := s.parent.MatchDocuments(ref)
	if err != nil {
		return nil, err
	}
	for _, d := range pmatch {
		if !shadow[d.Path] {
			local = append(local, d)
		}
	}
	return local, nil
}

// DocText overlays: the branch's copy if present, else (unless tombstoned) the
// parent's.
func (s *Store) DocText(exactPath string, from, to, maxChars int) (DocContent, error) {
	local, err := s.docTextLocal(exactPath, from, to, maxChars)
	if err == nil || s.parent == nil {
		return local, err
	}
	// Not in the branch — fall through to parent unless the path is tombstoned.
	ts, terr := s.q.ListTombstones(context.Background())
	if terr != nil {
		return DocContent{}, terr
	}
	for _, t := range ts {
		if t == exactPath {
			return DocContent{}, err // deleted-in-branch: stays "no document"
		}
	}
	return s.parent.DocText(exactPath, from, to, maxChars)
}

// DeleteDocument removes a document from THIS store and, when it's a branch, marks
// the path tombstoned so the parent's version stops showing through the overlay.
func (s *Store) DeleteDocument(path string) error {
	ctx := context.Background()
	if err := s.q.DeleteDocumentByPath(ctx, path); err != nil {
		return err
	}
	// Tombstone so a parent doc is hidden. Harmless on a non-branch store.
	return s.q.InsertTombstone(ctx, path)
}

// ── branch metadata (branch.json in the branch's scoped Home) ───────────

// BranchMeta records a branch's lineage + access times. Stored as branch.json in
// the branch's scoped Home; absent for non-branch indexes.
type BranchMeta struct {
	Parent         string `json:"parent"`
	CreatedAt      int64  `json:"created_at"`
	LastAccessedAt int64  `json:"last_accessed_at"`
}

func branchMetaPath(home Home) string { return filepath.Join(string(home), "branch.json") }

// readBranchMeta reads a home's branch.json; ok is false for a non-branch index.
func readBranchMeta(home Home) (BranchMeta, bool) {
	b, err := os.ReadFile(branchMetaPath(home))
	if err != nil {
		return BranchMeta{}, false
	}
	var m BranchMeta
	if json.Unmarshal(b, &m) != nil {
		return BranchMeta{}, false
	}
	return m, true
}

func writeBranchMeta(home Home, m BranchMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(branchMetaPath(home), b, 0o644)
}

// touchBranchMeta records a branch's last_accessed_at, throttled to at most once
// a minute so per-query reads don't rewrite branch.json constantly.
func touchBranchMeta(home Home) {
	if m, ok := readBranchMeta(home); ok {
		now := time.Now().UnixNano()
		if now-m.LastAccessedAt < int64(time.Minute) {
			return
		}
		m.LastAccessedAt = now
		_ = writeBranchMeta(home, m)
	}
}

// touchAccess records a read against this store when it's a branch (last-access
// reflects real queries, not the worker loop's Get calls).
func (s *Store) touchAccess() {
	if s.parent != nil {
		touchBranchMeta(s.home)
	}
}
