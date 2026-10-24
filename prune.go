package raglit

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prunable documents: documents whose source file is gone from disk.
//
// A deleted directory leaves its documents indexed, and they keep answering
// searches with text nobody can open (USER 2026-10-23: dun removes git
// worktrees all the time). Search leaves them out at once, because a hit you
// cannot open is worse than no hit; Prune deletes them, after a dry run that
// says how many and where. Nothing is recorded: the check is a stat, so a file
// that comes back (a remounted disk, a restored directory) is searchable again
// with no step in between.

// SourceFile is the file on disk a document was read from, and false when the
// document has none to check: a URL, or a relative path.
//
// A page range (slices.go, "<file>#p3-8") is its parent's file. A file inside
// another file (an archive member) is not detected here: its path does not
// exist even while the archive does, so it would read as missing. Members are
// written out to real files (emailattach.go), so none exist today.
func SourceFile(path string) (string, bool) {
	p := strings.TrimPrefix(path, "file://")
	if parent := ChildParent(p); parent != "" {
		p = parent
	}
	if !filepath.IsAbs(p) {
		return "", false
	}
	return p, true
}

// sourceGone reports that a document's source file no longer exists. Any
// error other than not-exist (permissions, a hung mount) is NOT gone: hiding
// documents on a transient error would empty a search for no reason.
func sourceGone(path string) bool {
	f, ok := SourceFile(path)
	if !ok {
		return false
	}
	_, err := os.Stat(f)
	return errors.Is(err, os.ErrNotExist)
}

// fromFileSQL is true for a document the ingest pipeline read from a source: it
// stamps frag_mode and content_hash. A document a library caller handed to
// Ingest has neither, and its path is a name, not a file; it is never
// prunable.
const fromFileSQL = `(d.frag_mode <> '' OR d.content_hash <> '')`

// missingSource reports a document read from a local file that is gone. An
// upload or extracted attachment is never missing: raglit holds its bytes, and
// prune does not delete them (USER 2026-10-24).
func (s *Store) missingSource(path string) bool {
	return sourceGone(path) && !s.inOwnStorage(path)
}

// gone reports a hit on a document whose source file is gone.
func (s *Store) gone(h Hit) bool { return h.fromFile && s.missingSource(h.Path) }

// dropGone removes hits whose source file is gone, keeping order.
func (s *Store) dropGone(hits []Hit) []Hit {
	out := hits[:0]
	for _, h := range hits {
		if !s.gone(h) {
			out = append(out, h)
		}
	}
	return out
}

// MissingDocuments is this layer's documents whose local source file is gone,
// among those read from a file (fromFileSQL).
func (s *Store) MissingDocuments() ([]string, error) {
	rows, err := s.db.Query(`SELECT d.path FROM documents d WHERE ` + fromFileSQL + ` ORDER BY d.path`)
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
		if s.missingSource(p) {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

type missingCache struct {
	mu sync.Mutex
	at time.Time
	n  int
}

// missingTTL is how stale a prunable count may be. A minute: the count is a
// prompt to run prune, not a figure anything acts on unseen.
const missingTTL = time.Minute

// MissingCount is len(MissingDocuments), cached for missingTTL; -1 when the
// count failed. Prune resets it.
func (s *Store) MissingCount() int {
	s.missing.mu.Lock()
	defer s.missing.mu.Unlock()
	if !s.missing.at.IsZero() && time.Since(s.missing.at) < missingTTL {
		return s.missing.n
	}
	docs, err := s.MissingDocuments()
	if err != nil {
		return -1
	}
	s.missing.at, s.missing.n = time.Now(), len(docs)
	return s.missing.n
}

// PruneDir is one directory's share of a prune.
type PruneDir struct {
	Dir       string `json:"dir"`
	Documents int    `json:"documents"`
}

// PruneResult is what Prune removes, or would remove.
type PruneResult struct {
	// Documents whose local file was not found.
	Documents int        `json:"documents"`
	Dirs      []PruneDir `json:"dirs"`
	// Removed is false on a dry run.
	Removed bool `json:"removed"`
	// Page and figure images under pages/ that no document names. Removed with
	// the documents.
	OrphanPages     int   `json:"orphan_pages"`
	OrphanPageBytes int64 `json:"orphan_page_bytes"`
	// Copies under originals/ (Home.OriginalsDir), except an upload's.
	DanglingOriginals     int   `json:"dangling_originals"`
	DanglingOriginalBytes int64 `json:"dangling_original_bytes"`
	// OriginalsRemoved is true when deleteOriginals removed them.
	OriginalsRemoved bool `json:"originals_removed"`
}

// Prune removes every document whose local source file is gone, with its
// fragments, vectors, pages, fields, caption rows and page images, and pending
// ingest jobs on those files, and the page images no document names. dry
// reports the same and removes nothing. deleteOriginals removes the dangling
// copies under originals/, dry or not: they are reported always and removed
// only when asked.
//
// Dirs groups by the top-most directory that is gone, so a deleted worktree is
// one line however deep its tree was; a file deleted from a directory that
// still exists is counted under that directory.
func (s *Store) Prune(dry, deleteOriginals bool) (PruneResult, error) {
	res := PruneResult{Dirs: []PruneDir{}}
	docs, err := s.MissingDocuments()
	if err != nil {
		return res, err
	}
	pages, err := s.orphanPages()
	if err != nil {
		return res, err
	}
	originals, err := s.danglingOriginals()
	if err != nil {
		return res, err
	}
	res.OrphanPages = len(pages)
	for _, p := range pages {
		res.OrphanPageBytes += fileSize(p)
	}
	res.DanglingOriginals = len(originals)
	for _, p := range originals {
		res.DanglingOriginalBytes += fileSize(p)
	}
	if deleteOriginals {
		for _, p := range originals {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return res, err
			}
		}
		res.OriginalsRemoved = true
	}
	res.Documents = len(docs)
	byDir := map[string]int{}
	for _, d := range docs {
		f, _ := SourceFile(d)
		byDir[goneRoot(filepath.Dir(f))]++
	}
	for d, n := range byDir {
		res.Dirs = append(res.Dirs, PruneDir{Dir: d, Documents: n})
	}
	sort.Slice(res.Dirs, func(i, j int) bool {
		if res.Dirs[i].Documents != res.Dirs[j].Documents {
			return res.Dirs[i].Documents > res.Dirs[j].Documents
		}
		return res.Dirs[i].Dir < res.Dirs[j].Dir
	})
	if dry {
		return res, nil
	}
	root := s.PagesRoot()
	for _, p := range pages {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
		removeEmptyDir(filepath.Dir(p), root)
	}
	if err := s.removeDocuments(docs, false); err != nil {
		return res, err
	}
	s.missing.mu.Lock()
	s.missing.at = time.Time{}
	s.missing.mu.Unlock()
	res.Removed = true
	return res, nil
}

// goneRoot walks up from dir while the parent is gone too, and returns the
// top-most missing directory; dir itself when it exists.
func goneRoot(dir string) string {
	if _, err := os.Stat(dir); err == nil {
		return dir
	}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		if _, err := os.Stat(parent); err == nil {
			return dir
		}
		dir = parent
	}
}

// orphanPages is every file under this index's pages/ that no page or figure
// row names and whose directory is no document's (Home.PageDir). Both, not
// either: a document moved by attachmigrate keeps rows naming its old
// directory until it is read again, and its files must not read as orphans.
func (s *Store) orphanPages() ([]string, error) {
	root := s.PagesRoot()
	if root == "" {
		return nil, nil
	}
	named := map[string]bool{}
	rows, err := s.db.Query(`SELECT image_path FROM ocr_pages WHERE image_path <> ''
		UNION SELECT image_path FROM media WHERE image_path <> ''`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		named[s.rerootPageImage(p)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	dirs, err := s.documentTags()
	if err != nil {
		return nil, err
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := pathUnder(p, root)
		if top, _, _ := strings.Cut(rel, string(filepath.Separator)); !named[p] && !dirs[top] {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// danglingOriginals is every copy under originals/, except one whose document
// is still indexed from raglit's own storage: that is an upload's, and prune
// does not delete uploads (USER 2026-10-24). Nothing reads originals/, so any
// other copy is dangling.
func (s *Store) danglingOriginals() ([]string, error) {
	if !s.withHome {
		return nil, nil
	}
	ents, err := os.ReadDir(s.home.OriginalsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	uploads := map[string]bool{}
	paths, err := s.documentPaths()
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		if s.inOwnStorage(p) {
			uploads[tag(p)] = true
		}
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && !uploads[e.Name()] {
			out = append(out, filepath.Join(s.home.OriginalsDir(), e.Name()))
		}
	}
	return out, nil
}

// documentTags is the Home.PageDir name of every document in this layer.
func (s *Store) documentTags() (map[string]bool, error) {
	paths, err := s.documentPaths()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[tag(p)] = true
	}
	return out, nil
}

func (s *Store) documentPaths() ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM documents`)
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
		out = append(out, p)
	}
	return out, rows.Err()
}
