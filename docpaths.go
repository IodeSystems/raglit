package raglit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Document paths, cheaply, and the ones raglit itself keeps.
//
// Two callers need the set of paths and nothing else: the daemon's index root
// (the deepest directory every corpus document shares) and the lookup that turns
// a root-relative path from the UI back into the indexed one. Documents() answers
// both by counting every document's fragments and engines, which is the wrong
// price for a question about strings.

// DocumentPaths lists every document path in the index, branch overlay applied:
// the branch's own documents, then the parent's that the branch does not shadow.
func (s *Store) DocumentPaths() ([]string, error) {
	paths, err := s.q.ListDocumentPaths(context.Background())
	if err != nil || s.parent == nil {
		return paths, err
	}
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	ppaths, err := s.parent.DocumentPaths()
	if err != nil {
		return nil, err
	}
	for _, p := range ppaths {
		if !shadow[p] {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// StorageRel reports whether a document's bytes live in raglit's own storage for
// this index (extracted attachments, uploads, transcripts) rather than in the
// corpus, and if so its path relative to that storage.
//
// Such documents are real documents, but their directory is raglit's business:
// letting ~/.raglit/indexes/<name>/attachments/... take part in the corpus's
// common directory widened one legal corpus's root to the user's home, which put
// /home/<user>/... in front of every path and made the review list walk the
// whole home directory.
//
// Two matches. Under this store's home is the ordinary case. The second is a
// home that has been COPIED or moved: the rows still carry the path of the home
// they were written in, and the only stable part of it is the
// ".../indexes/<this index>/" layout. A branch also asks its parent, whose
// documents show through it with the parent's home in their paths.
func (s *Store) StorageRel(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	if s.withHome {
		home := string(s.home)
		if abs, err := filepath.Abs(home); err == nil {
			home = abs
		}
		if rel, ok := pathUnder(path, home); ok {
			return rel, true
		}
		marker := string(filepath.Separator) + "indexes" + string(filepath.Separator) + filepath.Base(home) + string(filepath.Separator)
		if i := strings.LastIndex(path, marker); i >= 0 {
			return path[i+len(marker):], true
		}
	}
	if s.parent != nil {
		return s.parent.StorageRel(path)
	}
	return "", false
}

// StoragePath is where a storage document's bytes are on THIS machine: the
// file under this store's home (or its parent's, for a branch) that StorageRel
// names. Empty and false for a corpus document, for a path that would leave the
// home, and for a file that is not there.
func (s *Store) StoragePath(path string) (string, bool) {
	path = strings.TrimPrefix(path, "file://") // uploads are queued as file:// URLs
	if !filepath.IsAbs(path) {
		return "", false
	}
	if s.withHome {
		home := string(s.home)
		if abs, err := filepath.Abs(home); err == nil {
			home = abs
		}
		rel, ok := pathUnder(path, home)
		if !ok {
			marker := string(filepath.Separator) + "indexes" + string(filepath.Separator) + filepath.Base(home) + string(filepath.Separator)
			if i := strings.LastIndex(path, marker); i >= 0 {
				rel, ok = path[i+len(marker):], true
			}
		}
		if ok {
			full := filepath.Join(home, rel)
			if _, inside := pathUnder(full, home); inside {
				if fi, err := os.Stat(full); err == nil && fi.Mode().IsRegular() {
					return full, true
				}
			}
		}
	}
	if s.parent != nil {
		return s.parent.StoragePath(path)
	}
	return "", false
}

// pathUnder returns p relative to dir when p is strictly inside dir.
func pathUnder(p, dir string) (string, bool) {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// rerootPageImage maps a stored page-image path that points outside this home's
// pages/ directory back into it, by the part after the last "/pages/".
//
// ocr_pages stores the image path ABSOLUTE, as it was when the page was read. A
// home that is copied or moved keeps its rows and its images, but every image
// path still names the OLD home, so the page view showed a broken image for
// every page of every document. The layout under pages/ is deterministic
// (Home.PageDir), so the part after it is still right; only the prefix moved.
//
// Returned only when the re-rooted file exists: a guess that does not resolve is
// worse than the stored path, which at least says where the image used to be.
func (s *Store) rerootPageImage(img string) string {
	if img == "" || !s.withHome || !filepath.IsAbs(img) {
		return img
	}
	root := s.PagesRoot()
	if _, ok := pathUnder(img, root); ok {
		return img
	}
	marker := string(filepath.Separator) + "pages" + string(filepath.Separator)
	i := strings.LastIndex(img, marker)
	if i < 0 {
		return img
	}
	cand := filepath.Join(root, filepath.Clean(string(filepath.Separator)+img[i+len(marker):]))
	if _, err := os.Stat(cand); err != nil {
		return img
	}
	return cand
}
