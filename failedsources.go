package raglit

import (
	"os"
	"time"
)

// Failed sources: a file whose ingest failed is not queued again until its
// bytes change (IndexTree counts it Failed). Keyed by the job's url, with the
// hash of the bytes that failed; a hash, not an mtime, so a touch or a
// checkout of the same bytes does not retry it. Written when an ingest job
// ends in error, cleared when one for the path succeeds.

// recordFailedSource notes that path failed to ingest with these bytes.
func (s *Store) recordFailedSource(path, hash, msg string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, _ = s.db.Exec(`INSERT INTO failed_sources(path, hash, at, error) VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash, at=excluded.at, error=excluded.error`,
		path, hash, time.Now().UnixNano(), msg)
}

// clearFailedSource forgets a failure: the path ingested, or left the tree.
func (s *Store) clearFailedSource(path string) {
	_, _ = s.db.Exec(`DELETE FROM failed_sources WHERE path=?`, path)
}

// failedOn reports whether any of paths last failed on bytes with this hash.
func (s *Store) failedOn(hash string, paths ...string) bool {
	if hash == "" {
		return false
	}
	for _, p := range paths {
		var h string
		if s.db.QueryRow(`SELECT hash FROM failed_sources WHERE path=?`, p).Scan(&h) == nil && h == hash {
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
