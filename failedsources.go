package raglit

import (
	"os"
	"time"
)

// Failed sources: a file whose ingest failed is not queued again on the same
// bytes until a backoff passes (IndexTree counts it Failed). Keyed by the
// job's url, with the hash of the bytes that failed; a hash, not an mtime, so
// a touch or a checkout of the same bytes does not retry it early. Written
// when an ingest job ends in error, cleared when one for the path succeeds.
//
// The worker cannot tell a document's own failure (a broken PDF) from a
// model or transport one (embedder down, a timeout), so no failure is final:
// the backoff starts at failRetryBase and doubles with each failure on the
// same bytes, up to failRetryMax. An outage costs a wait, not the file.

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

// recordFailedSource notes that path failed to ingest with these bytes. A
// failure on the same bytes as the last one counts one more attempt.
func (s *Store) recordFailedSource(path, hash, msg string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, _ = s.db.Exec(`INSERT INTO failed_sources(path, hash, at, error, attempts) VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(path) DO UPDATE SET
			attempts=CASE WHEN failed_sources.hash=excluded.hash THEN failed_sources.attempts+1 ELSE 1 END,
			hash=excluded.hash, at=excluded.at, error=excluded.error`,
		path, hash, time.Now().UnixNano(), msg)
}

// clearFailedSource forgets a failure: the path ingested, or left the tree.
func (s *Store) clearFailedSource(path string) {
	_, _ = s.db.Exec(`DELETE FROM failed_sources WHERE path=?`, path)
}

// failedOn reports whether any of paths last failed on bytes with this hash
// and is still inside its backoff.
func (s *Store) failedOn(hash string, paths ...string) bool {
	if hash == "" {
		return false
	}
	now := time.Now()
	for _, p := range paths {
		var h string
		var at int64
		var attempts int
		if s.db.QueryRow(`SELECT hash, at, attempts FROM failed_sources WHERE path=?`, p).Scan(&h, &at, &attempts) != nil || h != hash {
			continue
		}
		if now.Before(time.Unix(0, at).Add(failBackoff(attempts))) {
			return true
		}
	}
	return false
}

// sourceHash is the hash of a job url's local bytes, "" when it has none.
func sourceHash(url string) string {
	p, ok := localPathOf(url)
	if !ok {
		return ""
	}
	// A file too large to hash cheaply stays unrecorded, which only means it
	// is retried.
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() || fi.Size() > 256<<20 {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return sha256hex(b)
}
