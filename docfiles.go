package raglit

import (
	"os"
	"path/filepath"
	"strings"
)

// The files raglit writes for a document, in this index's own storage.
//
// A document's rows were the only thing a delete removed. Its page and figure
// images stayed under pages/, and an uploaded document's bytes stayed under
// uploads/, for a document no longer in the index. Measured 2026-10-24 on the
// live daemon: 146 page files (20.7 MB) belonged to no document.
//
// Two kinds of document, and only one is raglit's to delete. A local-disk
// document is the user's file: raglit reads it, never removes it. An upload
// (or an extracted attachment) lives in raglit's storage (StorageRel); the
// index is its only home, so forgetting it removes it.

// RemoveDocument deletes a document as DeleteDocument does, and the page and
// figure images it made under this index's pages/. stored also deletes the
// document's own bytes when they are in this index's storage (an upload, an
// extracted attachment). A file outside the index's storage is never touched,
// whatever stored says. A copy under originals/ is left for prune's
// --delete-orphan-originals.
func (s *Store) RemoveDocument(path string, stored bool) error {
	imgs := s.documentImages(path)
	own := ""
	if stored {
		own = s.ownStoredFile(path)
	}
	if err := s.DeleteDocument(path); err != nil {
		return err
	}
	for _, img := range imgs {
		s.removePageFile(img)
	}
	removeEmptyDir(s.home.PageDir(path), s.PagesRoot())
	if own != "" {
		_ = os.Remove(own)
	}
	return nil
}

// documentImages is every image path a document's page and figure rows name.
func (s *Store) documentImages(path string) []string {
	rows, err := s.db.Query(`
		SELECT o.image_path FROM ocr_pages o JOIN documents d ON d.id = o.doc_id
		 WHERE d.path = ? AND o.image_path <> ''
		UNION
		SELECT m.image_path FROM media m JOIN documents d ON d.id = m.doc_id
		 WHERE d.path = ? AND m.image_path <> ''`, path, path)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// removePageFile deletes one image a removed document named, when it is under
// this index's pages/ and no remaining row names it.
func (s *Store) removePageFile(img string) {
	root := s.PagesRoot()
	if root == "" {
		return
	}
	p := s.rerootPageImage(img)
	if _, ok := pathUnder(p, root); !ok {
		return
	}
	var n int
	if err := s.db.QueryRow(`
		SELECT (SELECT count(*) FROM ocr_pages WHERE image_path IN (?, ?))
		     + (SELECT count(*) FROM media WHERE image_path IN (?, ?))`,
		img, p, img, p).Scan(&n); err != nil || n > 0 {
		return
	}
	_ = os.Remove(p)
	removeEmptyDir(filepath.Dir(p), root)
}

// ownStoredFile is the document's bytes in THIS index's storage, "" when they
// are elsewhere: the user's disk, or a parent's storage seen through a branch.
func (s *Store) ownStoredFile(path string) string {
	p, ok := s.StoragePath(path)
	if !ok || !s.withHome {
		return ""
	}
	home, err := filepath.Abs(string(s.home))
	if err != nil {
		return ""
	}
	if _, under := pathUnder(p, home); !under {
		return ""
	}
	return p
}

// inOwnStorage reports a document whose bytes raglit holds (an upload, an
// extracted attachment) rather than a file on the user's disk.
func (s *Store) inOwnStorage(path string) bool {
	_, ok := s.StorageRel(strings.TrimPrefix(path, "file://"))
	return ok
}

// removeEmptyDir removes dir and then each empty parent, stopping at root,
// which stays.
func removeEmptyDir(dir, root string) {
	if root == "" {
		return
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		if _, ok := pathUnder(dir, root); !ok {
			return
		}
		if os.Remove(dir) != nil {
			return // not empty, or not there
		}
		dir = filepath.Dir(dir)
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
