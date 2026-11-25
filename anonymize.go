package raglit

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// The anonymize export: what makes a quality log safe to leave the machine.
//
// The log itself is local and full of real queries, paths and document ids.
// An export replaces every identifier and piece of text with a stable hash —
// stable within one export, so a session id still joins its search to its
// fetch, and per-export, so two exports of the same log share no values and
// no export can be merged back into the one that made it. What is kept is the
// quality signal: ranks, scores, origins, sizes, and which hits were fetched
// or cited. Nothing here re-derives an original; a hash is one-way.

// AnonKey is one export's salt. Every hash it makes is
// sha256(salt || kind || 0 || value) cut to 16 hex characters: the kind tag
// keeps a path and a query with the same text from sharing a value, and 64
// bits is more than an export of this size needs.
type AnonKey struct{ salt [16]byte }

// NewAnonKey draws a fresh key from the OS random source, so two exports of
// the same log are unrelated.
func NewAnonKey() (AnonKey, error) {
	var k AnonKey
	if _, err := rand.Read(k.salt[:]); err != nil {
		return k, err
	}
	return k, nil
}

// Hash returns the export-stable name for one value.
func (k AnonKey) Hash(kind, value string) string {
	h := sha256.New()
	h.Write(k.salt[:])
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil)[:])[:16]
}

// AnonymizeEvent returns the export form of an event: paths, document ids,
// query text and session ids replaced by this export's hashes; ranks, scores,
// origins, char counts and fetch/use flags unchanged. A session and the
// FetchedBy that names it get the SAME hash, so search → fetch → use chains
// still join inside the export.
func AnonymizeEvent(k AnonKey, ev QualityEvent) QualityEvent {
	if ev.Query != "" {
		ev.Query = k.Hash("query", ev.Query)
	}
	if ev.Session != "" {
		ev.Session = k.Hash("session", ev.Session)
	}
	if ev.Path != "" {
		ev.Path = k.Hash("path", ev.Path)
	}
	if ev.DocID != "" {
		ev.DocID = k.Hash("path", ev.DocID)
	}
	if ev.FetchedBy != "" {
		ev.FetchedBy = k.Hash("session", ev.FetchedBy)
	}
	for i := range ev.Hits {
		if ev.Hits[i].DocID != "" {
			ev.Hits[i].DocID = k.Hash("path", ev.Hits[i].DocID)
		}
	}
	return ev
}

// ReadQualityLog reads a project's quality log back: the rotated generations
// oldest first (.3, .2, .1) and the current file last. Missing files are
// skipped, so a log that never rotated reads from one file and a project
// whose log is off reads nothing.
func ReadQualityLog(home Home) ([]QualityEvent, error) {
	var evs []QualityEvent
	for _, n := range []int{3, 2, 1, 0} {
		path := home.QualityLogPath()
		if n > 0 {
			path = fmt.Sprintf("%s.%d", path, n)
		}
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		lineNo := 0
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			lineNo++
			var ev QualityEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				f.Close()
				return nil, fmt.Errorf("%s:%d: not a quality log event: %w", path, lineNo, err)
			}
			evs = append(evs, ev)
		}
		terr := sc.Err()
		f.Close()
		if terr != nil {
			return nil, terr
		}
	}
	return evs, nil
}

// AnonymizeExport is the whole export: every event of a project's log in the
// form it leaves the machine. The caller gets the events and writes them; the
// command layer is what shows what leaves before it leaves.
func AnonymizeExport(home Home, k AnonKey) ([]QualityEvent, error) {
	evs, err := ReadQualityLog(home)
	if err != nil {
		return nil, err
	}
	out := make([]QualityEvent, len(evs))
	for i := range evs {
		out[i] = AnonymizeEvent(k, evs[i])
	}
	return out, nil
}
