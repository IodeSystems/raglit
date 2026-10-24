package raglit

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Operator controls on one index: pause, ignore patterns, purge by pattern.
//
// USER 2026-10-22: "we desperately need a pause index … I'm burning money on
// node_modules." A daemon spending model calls on the wrong files had no stop
// short of killing it, which stops every index.

const metaPaused = "paused"

// Paused reports whether this index runs no work. Jobs still queue while it is
// paused, and run when it is resumed.
func (s *Store) Paused() bool {
	v, _ := s.Meta(metaPaused)
	return v == "1"
}

// SetPaused pauses or resumes this index. Stored in the index, so a restart
// keeps it. A job already running finishes; nothing new is claimed.
func (s *Store) SetPaused(paused bool) error {
	v := ""
	if paused {
		v = "1"
	}
	return s.SetMeta(metaPaused, v, time.Now().Unix())
}

// ErrIgnored is Enqueue refusing a path an ignore pattern matches. Callers that
// enqueue many paths skip it and count it: one ignored file must not fail the
// batch it came in.
var ErrIgnored = errors.New("raglit: ignored")

const (
	metaIgnore = "ignore"
	metaAllow  = "allow"
)

// enqueueIgnore is what every index refuses at Enqueue, whatever its own list.
//
// Narrower than builtinIgnore on purpose. builtinIgnore is matched relative to
// a configured root, where a dot-directory is the repo's own tooling. Enqueue
// sees absolute paths, and indexes legitimately hold files under dot-dirs —
// skills under ~/.claude, uploads under ~/.raglit — so only the rule nothing
// legitimate breaks is global. Measured cost of not having it: one dun session
// queued ~7,400 files from worktree node_modules, ~1,900 of them for captions.
//
// Mobile build output is global for the same reason (USER 2026-10-22): a
// React Native tree's android/app/build holds a copy of every image per build
// variant, and images go to the vision model. A bare `build` is NOT here: a
// corpus folder named "build" is legitimate, so only the platform-qualified
// paths are.
//
// Agent worktrees are global too (USER 2026-10-23): a git worktree under
// .claude/worktrees or .dun/worktrees is a full copy of the repo, so indexing
// one returns every hit twice, and an agent run leaves dozens. Measured: 2,174
// of 2,582 documents in one dun index were worktree copies. These are
// specific dot-dir paths, not dot-dirs in general, so ~/.claude/skills stays
// indexable.
var enqueueIgnore = []string{
	"**/node_modules/**",
	"**/.claude/worktrees/**",
	"**/.dun/worktrees/**",
	"**/.gradle/**",
	"**/Pods/**",
	"**/android/build/**",
	"**/android/app/build/**",
	"**/ios/build/**",
	"**/.expo/**",
}

// IgnorePatterns is this index's own ignore list: globs a path must not match to
// be enqueued. Matched against the absolute path, so a directory anywhere is
// `**/name/**`; a glob with no "/" matches the file name.
func (s *Store) IgnorePatterns() []string { return s.globList(metaIgnore) }

// AllowPatterns is this index's allow list: a path matching one is exempt from
// the global ignores (EnqueueBuiltinIgnore), so an index that IS an iOS
// project can take `**/Pods/**`. The index's own ignore list still applies —
// both are this index's choices, and refusing is the safe way to settle them.
func (s *Store) AllowPatterns() []string { return s.globList(metaAllow) }

func (s *Store) globList(key string) []string {
	v, ok := s.Meta(key)
	if !ok || v == "" {
		return []string{}
	}
	var out []string
	if json.Unmarshal([]byte(v), &out) != nil {
		return []string{}
	}
	return out
}

// SetIgnorePatterns replaces this index's ignore list. Blank entries and
// repeats are dropped. It does not touch documents already indexed; a purge
// does that.
func (s *Store) SetIgnorePatterns(patterns []string) ([]string, error) {
	return s.setGlobList(metaIgnore, patterns)
}

// SetAllowPatterns replaces this index's allow list, cleaned the same way.
func (s *Store) SetAllowPatterns(patterns []string) ([]string, error) {
	return s.setGlobList(metaAllow, patterns)
}

func (s *Store) setGlobList(key string, patterns []string) ([]string, error) {
	seen := map[string]bool{}
	clean := []string{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if _, err := regexp.Compile(globToRegexp(p)); err != nil {
			return nil, fmt.Errorf("raglit: %s pattern %q: %w", key, p, err)
		}
		seen[p] = true
		clean = append(clean, p)
	}
	b, _ := json.Marshal(clean)
	return clean, s.SetMeta(key, string(b), time.Now().Unix())
}

// IgnoredBy returns the pattern that refuses path at Enqueue, "" when none does.
// The index's own ignore list first; then the global list, unless the index's
// allow list exempts the path.
func (s *Store) IgnoredBy(path string) string {
	path = strings.TrimPrefix(path, "file://")
	for _, pat := range s.IgnorePatterns() {
		if matchAny([]string{pat}, path) {
			return pat
		}
	}
	for _, pat := range enqueueIgnore {
		if matchAny([]string{pat}, path) {
			if matchAny(s.AllowPatterns(), path) {
				return ""
			}
			return pat
		}
	}
	return ""
}

// EnqueueBuiltinIgnore is the ignore list every index applies, for display.
func EnqueueBuiltinIgnore() []string { return append([]string{}, enqueueIgnore...) }

// PurgeResult is what a purge by pattern removes, or would remove.
type PurgeResult struct {
	Pattern   string   `json:"pattern"`
	Documents int      `json:"documents"`
	Sample    []string `json:"sample"`
	// Pending jobs on matching paths, dropped with the documents.
	IngestJobs  int `json:"ingest_jobs"`
	CaptionJobs int `json:"caption_jobs"`
}

const purgeSample = 20

// Purge removes every document whose path matches pattern, with its fragments,
// vectors, pages, fields and page images, and drops pending ingest and caption
// jobs on matching paths. dry reports the same counts and removes nothing. Files
// on the user's disk are untouched; an upload's file goes with its document
// (RemoveDocument). Matched like an ignore pattern: against the absolute
// path, or the file name for a glob with no "/".
func (s *Store) Purge(pattern string, dry bool) (PurgeResult, error) {
	pattern = strings.TrimSpace(pattern)
	res := PurgeResult{Pattern: pattern, Sample: []string{}}
	if pattern == "" {
		return res, fmt.Errorf("raglit: purge: empty pattern")
	}
	if _, err := regexp.Compile(globToRegexp(pattern)); err != nil {
		return res, fmt.Errorf("raglit: purge pattern %q: %w", pattern, err)
	}
	match := func(p string) bool { return matchAny([]string{pattern}, strings.TrimPrefix(p, "file://")) }
	paths := func(q string) ([]string, error) {
		rows, err := s.db.Query(q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return nil, err
			}
			if match(p) {
				out = append(out, p)
			}
		}
		return out, rows.Err()
	}
	docs, err := paths(`SELECT path FROM documents ORDER BY path`)
	if err != nil {
		return res, err
	}
	ingest, err := paths(`SELECT DISTINCT url FROM ingest_jobs WHERE state='pending'`)
	if err != nil {
		return res, err
	}
	captions, err := paths(`SELECT path FROM identity_jobs WHERE state='pending'`)
	if err != nil {
		return res, err
	}
	res.Documents, res.IngestJobs, res.CaptionJobs = len(docs), len(ingest), len(captions)
	res.Sample = append(res.Sample, docs[:min(len(docs), purgeSample)]...)
	if dry {
		return res, nil
	}
	for _, u := range ingest {
		if _, err := s.db.Exec(`DELETE FROM ingest_jobs WHERE url=? AND state='pending'`, u); err != nil {
			return res, err
		}
	}
	for _, p := range captions {
		if _, err := s.db.Exec(`DELETE FROM identity_jobs WHERE path=? AND state='pending'`, p); err != nil {
			return res, err
		}
	}
	return res, s.removeDocuments(docs, true)
}

// removeDocuments deletes documents with everything hung off them, their caption
// rows, and pending ingest jobs on their paths. Purge's and Prune's shared tail;
// stored is RemoveDocument's.
func (s *Store) removeDocuments(docs []string, stored bool) error {
	for _, p := range docs {
		if err := s.RemoveDocument(p, stored); err != nil {
			return fmt.Errorf("raglit: remove %s: %w", p, err)
		}
		// A caption row for a document that no longer exists is history about
		// nothing.
		if _, err := s.db.Exec(`DELETE FROM identity_jobs WHERE path=? AND state<>'running'`, p); err != nil {
			return err
		}
		if _, err := s.db.Exec(`DELETE FROM ingest_jobs WHERE url IN (?, ?) AND state='pending'`, p, "file://"+p); err != nil {
			return err
		}
	}
	return nil
}
