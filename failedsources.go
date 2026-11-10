package raglit

import (
	"os"
	"strings"
	"time"
)

// Failed sources: a file whose ingest failed is not queued again on the same
// bytes (IndexTree counts it Failed). Keyed by the job's url, with the hash of
// the bytes that failed; a hash, not an mtime, so a touch or a checkout of the
// same bytes does not retry it. Written when an ingest job ends in error,
// cleared when one for the path succeeds, and by ClearFailed.
//
// Each failure has a kind (Carl 2026-11-10, ragcore.md):
//   - final: the document's own failure (extract or parse failure,
//     unsupported or unreadable content). Not retried until the bytes change.
//   - transient: outside the document (IsTransient: a model or fetch endpoint
//     down, slow, or refusing for load). Retried after a backoff that starts
//     at failRetryBase and doubles with each failure on the same bytes, up to
//     failRetryMax. An outage costs a wait, not the file.
//
// A row made before the kind column existed is transient: it was recorded
// when no failure was final.

const (
	failFinal     = "final"
	failTransient = "transient"
)

const (
	failRetryBase = 15 * time.Minute
	failRetryMax  = 24 * time.Hour
)

// failBackoff is how long a source that failed attempts times on the same
// bytes waits before IndexTree queues it again.
func failBackoff(attempts int) time.Duration {
	d := failRetryBase
	for i := 1; i < attempts && d < failRetryMax; i++ {
		d *= 2
	}
	if d > failRetryMax {
		d = failRetryMax
	}
	return d
}

// recordFailedSource notes that path failed to ingest with these bytes, with
// kind failFinal or failTransient. A failure on the same bytes as the last one
// counts one more attempt.
func (s *Store) recordFailedSource(path, hash, msg, kind string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, _ = s.db.Exec(`INSERT INTO failed_sources(path, hash, at, error, attempts, kind) VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(path) DO UPDATE SET
			attempts=CASE WHEN failed_sources.hash=excluded.hash THEN failed_sources.attempts+1 ELSE 1 END,
			hash=excluded.hash, at=excluded.at, error=excluded.error, kind=excluded.kind`,
		path, hash, time.Now().UnixNano(), msg, kind)
}

// failKind is the failed_sources kind of an ingest error.
func failKind(err error) string {
	if IsTransient(err) {
		return failTransient
	}
	return failFinal
}

// clearFailedSource forgets a failure: the path ingested, or left the tree.
func (s *Store) clearFailedSource(path string) {
	_, _ = s.db.Exec(`DELETE FROM failed_sources WHERE path=?`, path)
}

// ClearFailed forgets the recorded ingest failures of paths (document paths,
// bare or file://; either spelling clears both), or of every path when none is
// given, so the next IndexTree queues those files again on the same bytes.
func (s *Store) ClearFailed(paths ...string) error {
	if len(paths) == 0 {
		_, err := s.db.Exec(`DELETE FROM failed_sources`)
		return err
	}
	for _, p := range paths {
		bare := strings.TrimPrefix(p, "file://")
		if _, err := s.db.Exec(`DELETE FROM failed_sources WHERE path IN (?, ?)`, bare, "file://"+bare); err != nil {
			return err
		}
	}
	return nil
}

// failedOn reports whether any of paths last failed on bytes with this hash
// and is not to be retried yet: a final failure, or a transient one still
// inside its backoff.
func (s *Store) failedOn(hash string, paths ...string) bool {
	if hash == "" {
		return false
	}
	now := time.Now()
	for _, p := range paths {
		var h, kind string
		var at int64
		var attempts int
		if s.db.QueryRow(`SELECT hash, at, attempts, kind FROM failed_sources WHERE path=?`, p).Scan(&h, &at, &attempts, &kind) != nil || h != hash {
			continue
		}
		if kind == failFinal || now.Before(time.Unix(0, at).Add(failBackoff(attempts))) {
			return true
		}
	}
	return false
}

// sourceHash is the hash of a job url's local bytes, "" when it has none. It
// reads the file in pieces, so a file of any size is recorded: one left
// unrecorded would be queued again on every IndexTree pass.
func sourceHash(url string) string {
	p, ok := localPathOf(url)
	if !ok {
		return ""
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	sc, err := scanFile(p)
	if err != nil {
		return ""
	}
	return sc.hash
}
