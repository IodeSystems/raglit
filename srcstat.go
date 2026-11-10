package raglit

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"
)

// The source file's size and modified time, per document.
//
// USER 2026-10-22: the Documents tree rolls up size and the earliest and
// latest date per folder. The date is the FILE's modified time: what period a
// folder covers, which the ingest time (added_at) does not say. Recorded when an
// ingest job finishes, and backfilled at daemon start for documents ingested
// before the columns existed.

// IndexTree skips hashing a file whose size and modified time (ns) equal the
// stat recorded when its bytes were read (Carl 2026-11-10, ragcore.md). That
// stat is src_size and src_mtime_ns, written only by an ingest (taken BEFORE
// the fetch, so a write during the ingest leaves a stat that differs next
// pass) and by an IndexTree pass that hashed the file and found the stored
// hash. src_mtime (seconds) is the Documents tree's date and is also written by
// RecordSourceStat and BackfillSourceStats, which do not vouch for the bytes;
// those leave src_mtime_ns at 0, so the file is hashed.
//
// A file modified less than racyWindow before its stat was taken is not
// vouched for either (ns 0): a write in the same filesystem clock tick, with
// the same size, would leave the stat unchanged. Git's "racily clean" rule.

// racyWindow covers a coarse filesystem clock (a jiffy on Linux, 2 s on FAT).
const racyWindow = 2 * time.Second

// srcStat is a file's stat as the skip compares it. mtimeNS 0 = do not skip.
type srcStat struct {
	size, mtime, mtimeNS int64
}

// statAt is fi as a srcStat, taken at time at.
func statAt(fi os.FileInfo, at time.Time) srcStat {
	st := srcStat{size: fi.Size(), mtime: fi.ModTime().Unix(), mtimeNS: fi.ModTime().UnixNano()}
	if at.Sub(fi.ModTime()) < racyWindow {
		st.mtimeNS = 0
	}
	return st
}

// localStat is the stat of the regular file behind a document path or job
// url, now; false for a URL, an upload, or a file that is not there.
func localStat(path string) (srcStat, bool) {
	local := localSource(path)
	if local == "" {
		return srcStat{}, false
	}
	fi, err := os.Stat(local)
	if err != nil || !fi.Mode().IsRegular() {
		return srcStat{}, false
	}
	return statAt(fi, time.Now()), true
}

// stamp is a file's size and full modified time (ns), with no racy-window
// rule: it tells whether the file changed between two stats.
type stamp struct{ size, mtimeNS int64 }

// fileStamp is the stamp of the regular file behind a document path or job
// url; the zero stamp and false for a URL, an upload, or a file not there.
func fileStamp(path string) (stamp, bool) {
	local := localSource(path)
	if local == "" {
		return stamp{}, false
	}
	fi, err := os.Stat(local)
	if err != nil || !fi.Mode().IsRegular() {
		return stamp{}, false
	}
	return stamp{fi.Size(), fi.ModTime().UnixNano()}, true
}

// recordReadStat stores st, the stat of the file whose bytes the document at
// path now holds. On a branch, a document only the parent holds has no row
// here: its stat goes to branch_stats, with the parent's hash it vouches for.
func (s *Store) recordReadStat(path string, st srcStat) error {
	res, err := s.db.Exec(`UPDATE documents SET src_size = ?, src_mtime = ?, src_mtime_ns = ? WHERE path IN (?, ?)`,
		st.size, st.mtime, st.mtimeNS, path, localSource(path))
	if err != nil || !s.worktree() {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	hash, err := s.DocumentHash(path)
	if err != nil || hash == "" {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO branch_stats(path, hash, src_size, src_mtime_ns) VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash, src_size=excluded.src_size, src_mtime_ns=excluded.src_mtime_ns`,
		path, hash, st.size, st.mtimeNS)
	return err
}

// readStat is the stat recorded for the document at path by recordReadStat,
// false when there is none. On a branch, a document it shows from the parent
// has the stat in branch_stats, valid while the parent's hash is the one it
// was recorded with. The parent's own row is read only for a path outside
// both roots, where the parent's file is this file: under the root, the
// parent's row is the stat of the main checkout's copy.
func (s *Store) readStat(path string) (srcStat, bool, error) {
	var st srcStat
	err := s.db.QueryRow(`SELECT src_size, src_mtime, src_mtime_ns FROM documents WHERE path = ?`, path).
		Scan(&st.size, &st.mtime, &st.mtimeNS)
	if errors.Is(err, sql.ErrNoRows) {
		if s.worktree() {
			tomb, terr := s.tombstoned(path)
			if terr != nil {
				return srcStat{}, false, terr
			}
			pp, ok := s.toParent(path)
			if !ok || tomb {
				return srcStat{}, false, nil
			}
			var hash string
			berr := s.db.QueryRow(`SELECT hash, src_size, src_mtime_ns FROM branch_stats WHERE path = ?`, path).
				Scan(&hash, &st.size, &st.mtimeNS)
			if berr == nil {
				if now, herr := s.parent.DocumentHash(pp); herr != nil || now != hash {
					return srcStat{}, false, herr
				}
				return st, st.mtimeNS != 0, nil
			}
			if !errors.Is(berr, sql.ErrNoRows) {
				return srcStat{}, false, berr
			}
			if pp == path {
				return s.parent.readStat(pp)
			}
		}
		return srcStat{}, false, nil
	}
	if err != nil {
		return srcStat{}, false, err
	}
	return st, st.mtimeNS != 0, nil
}

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
// document at path, now. A path with no local file is left at 0 (unknown). It
// does not vouch for the stored bytes, so IndexTree hashes the file next pass.
func (s *Store) RecordSourceStat(path string) error {
	local := localSource(path)
	if local == "" {
		return nil
	}
	fi, err := os.Stat(local)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	_, err = s.db.Exec(`UPDATE documents SET src_size = ?, src_mtime = ?, src_mtime_ns = 0 WHERE path IN (?, ?)`,
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
