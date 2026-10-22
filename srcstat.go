package raglit

import (
	"os"
	"strings"
)

// The source file's size and modified time, per document.
//
// USER 2026-10-22: the Documents tree rolls up size and the earliest and
// latest date per folder. The date is the FILE's modified time: what period a
// folder covers, which the ingest time (added_at) does not say. Recorded when an
// ingest job finishes, and backfilled at daemon start for documents ingested
// before the columns existed.

// localSource is the file a document path names on this machine, "" for a URL
// or an upload.
func localSource(path string) string {
	p := strings.TrimPrefix(path, "file://")
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	return p
}

// RecordSourceStat stores the size and modified time of the file behind the
// document at path. A path with no local file is left at 0 (unknown).
func (s *Store) RecordSourceStat(path string) error {
	local := localSource(path)
	if local == "" {
		return nil
	}
	fi, err := os.Stat(local)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	_, err = s.db.Exec(`UPDATE documents SET src_size = ?, src_mtime = ? WHERE path IN (?, ?)`,
		fi.Size(), fi.ModTime().Unix(), path, local)
	return err
}

// BackfillSourceStats records the stat of every document whose file is unknown
// so far, and returns how many it filled. Files no longer on disk stay unknown.
func (s *Store) BackfillSourceStats() (int, error) {
	rows, err := s.db.Query(`SELECT path FROM documents WHERE src_mtime = 0`)
	if err != nil {
		return 0, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		if localSource(p) != "" {
			paths = append(paths, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, p := range paths {
		fi, err := os.Stat(localSource(p))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if _, err := s.db.Exec(`UPDATE documents SET src_size = ?, src_mtime = ? WHERE path = ?`,
			fi.Size(), fi.ModTime().Unix(), p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
