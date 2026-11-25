package raglit

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// A local, per-project event log of search and surfacing activity — the quality
// signal a bench cannot have: which queries a person actually asked, which hits
// came back, which of those were fetched or cited, and which went untouched.
//
// Off by default (a zero QualityLog config writes nothing, and no file is
// created). When on, one JSON line per event goes to <home>/quality-log.jsonl
// under the project's own home, and a file past its size cap rotates to the
// next generation — growth is bounded by the cap, never unbounded.
//
// The log is LOCAL: it is a diagnostic for the person running this corpus, and
// an anonymize step (a separate command) is what makes any of it exportable.

const defaultQualityLogMaxBytes = 1 << 20 // 1 MiB

// QualityHit is one hit a logged search returned, in the order it came back.
type QualityHit struct {
	DocID   string  `json:"doc_id"`
	Rank    int     `json:"rank"`
	Score   float64 `json:"score"`
	Origin  string  `json:"origin"` // body | identity | generated question
	Chars   int     `json:"chars"`  // the hit's text, in characters
}

// QualityEvent is one line of the log.
type QualityEvent struct {
	Type    string    `json:"type"` // search | get_document | surface | cite
	Project string    `json:"project"`
	Time    time.Time `json:"time"`
	Session string    `json:"session,omitempty"`

	// Search.
	Query  string       `json:"query,omitempty"`
	Mode   string       `json:"mode,omitempty"`
	Index  string       `json:"index,omitempty"` // the requested index scope, as sent
	Hits   []QualityHit `json:"hits,omitempty"`

	// Get-document fetch: what was read, and how much came back.
	Path      string `json:"path,omitempty"`
	DocID     string `json:"doc_id,omitempty"`
	IndexHit  string `json:"index_hit,omitempty"`
	Chars     int    `json:"chars,omitempty"`
	FetchedBy string `json:"fetched_by,omitempty"` // session that fetched it

	// Surfacing: the step that caused the surfacing (prompt / tool use).
	TriggerStep string `json:"trigger_step,omitempty"`
}

// qualityLogWriter appends JSONL events for one project, rotating at its size
// cap. A writer for a project whose log is off is never created: the hooks
// check the config first, which is what "off is the default and writes
// nothing" reduces to.
type QualityLogWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

// newQualityLogWriter opens (or creates) the log file for the project home,
// resuming its size from disk so a restarted daemon rotates where it left off
// rather than re-growing a file past its cap.
func NewQualityLogWriter(home Home, maxBytes int) (*QualityLogWriter, error) {
	if maxBytes <= 0 {
		maxBytes = defaultQualityLogMaxBytes
	}
	path := home.QualityLogPath()
	if err := os.MkdirAll(string(home), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &QualityLogWriter{path: path, maxBytes: int64(maxBytes), f: f, size: fi.Size()}, nil
}

// Record appends one event as a JSON line, rotating first when the file is at
// its cap. Rotation shifts the current file to the next generation (and the
// generations along behind it), so at most a handful of files of at most
// maxBytes exist at any time.
func (w *QualityLogWriter) Record(ev QualityEvent) error {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(b)) > w.maxBytes && w.size > 0 {
		if err := w.rotateLocked(); err != nil {
			return err
		}
	}
	if _, err := w.f.Write(b); err != nil {
		return err
	}
	w.size += int64(len(b))
	return nil
}

// rotateLocked moves the current file to .<n> and opens a fresh one. The
// generations shift: .1 → .2 → .3 (dropped at .3, so three files of at most
// maxBytes bound the total).
func (w *QualityLogWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	last := fmt.Sprintf("%s.%d", w.path, 3)
	if err := os.Remove(last); err != nil && !os.IsNotExist(err) {
		return err
	}
	for n := 2; n >= 1; n-- {
		src := fmt.Sprintf("%s.%d", w.path, n)
		dst := fmt.Sprintf("%s.%d", w.path, n+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	return nil
}

// Close closes the file. A closed writer records nothing.
func (w *QualityLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// HitFate is one hit from a search event, annotated with what happened to it:
// "fetched" (a get_document event for the same doc in the same session),
// "cited" (a cite event for the same doc in the same session — the answer
// referenced it), or "never_touched".
type HitFate struct {
	DocID  string  `json:"doc_id"`
	Rank   int     `json:"rank"`
	Score  float64 `json:"score"`
	Origin string  `json:"origin"`
	Chars  int     `json:"chars"`
	Fate   string  `json:"fate"` // fetched | cited | never_touched
}

// SearchJoin is one search event with its hits' fates resolved.
type SearchJoin struct {
	Event QualityEvent `json:"event"`
	Hits  []HitFate    `json:"hits"`
}

// JoinFetchesToSearch resolves what happened to each hit in each search event.
// A hit is "cited" if a cite event in the same session references the same
// doc_id at or after the search event's time. Otherwise it is "fetched" if a
// get_document event in the same session references the same doc_id at or
// after the search event's time. Otherwise it is "never_touched".
// Cited takes precedence over fetched: a document that was both fetched and
// cited in the answer is "cited".
func JoinFetchesToSearch(events []QualityEvent) []SearchJoin {
	var joins []SearchJoin
	for _, ev := range events {
		if ev.Type != "search" {
			continue
		}
		fetched := map[string]bool{}
		cited := map[string]bool{}
		for _, fe := range events {
			if fe.Session == "" || fe.Session != ev.Session {
				continue
			}
			if fe.Time.Before(ev.Time) {
				continue
			}
			switch fe.Type {
			case "get_document":
				fetched[fe.DocID] = true
			case "cite":
				cited[fe.DocID] = true
			}
		}
		hits := make([]HitFate, 0, len(ev.Hits))
		for _, h := range ev.Hits {
			fate := "never_touched"
			if fetched[h.DocID] {
				fate = "fetched"
			}
			if cited[h.DocID] {
				fate = "cited"
			}
			hits = append(hits, HitFate{DocID: h.DocID, Rank: h.Rank, Score: h.Score, Origin: h.Origin, Chars: h.Chars, Fate: fate})
		}
		joins = append(joins, SearchJoin{Event: ev, Hits: hits})
	}
	return joins
}
