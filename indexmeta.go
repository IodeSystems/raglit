package raglit

import (
	"io/fs"
	"path/filepath"
)

// IndexMeta is what a signed-in caller may know about an index it cannot read:
// who owns it, whether it is public, how much disk it takes and how its queue
// stands. No document, passage or path from inside it.
//
// The web UI lists every index with this (Carl 2026-11-04: "metadata only
// unless admin"), so an index a project key created is visible to the people
// running the daemon, as an index with an owner, rather than missing.
type IndexMeta struct {
	Owner      string
	Visibility Visibility
	Bytes      int64 // the index home on disk, every file in it
	Status     Status
}

// Meta reads an index's metadata without the read check. False when the index
// does not exist; nothing is created.
func (r *Registry) Meta(name string) (IndexMeta, bool) {
	name = normalizeIndexName(name)
	if !r.exists(name) {
		return IndexMeta{}, false
	}
	m := IndexMeta{}
	if r.ACLEnabled() {
		a := r.ACLOf(name)
		m.Owner, m.Visibility = a.Owner, a.Visibility
	}
	_ = filepath.WalkDir(string(r.indexHome(name)), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			m.Bytes += info.Size()
		}
		return nil
	})
	if st, err := r.Existing(name); err == nil {
		if s, err := st.IndexStatus(); err == nil {
			m.Status = s
		}
	}
	return m, true
}
