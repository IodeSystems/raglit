package raglit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The audit trail, and why it is the source rather than a copy.
//
// judgements.db is a projection. Every ruling in it — a pair called a copy, a
// page range called a document — arrives as an EVENT appended to
// raglit-audit.jsonl first, and the database is what you get by replaying those
// events in order. Delete the database and it rebuilds; delete the log and the
// decisions are gone.
//
// That ordering is the whole design, and it buys back everything a database
// costs in a corpus like this one:
//
//	git diff and git blame work, because the record of record is text. "Why did
//	this change?" stays answerable over a corpus where one corrected fact is
//	chased through a dozen documents.
//
//	Three machines syncing a folder merge appended lines without conflict
//	resolution. A binary SQLite file syncs by one side winning and the other
//	being renamed, which for decisions nobody wrote down twice is data loss.
//
//	The database can be dropped and rebuilt at will — after a schema change,
//	after corruption, or to check that it says what the log says.
//
// The write order is log-first and it is not arbitrary. Append, then apply: if
// the process dies between, the database is behind the log and a rebuild fixes
// it. Applying first and dying would leave a decision in the database that the
// log never recorded, which is the one failure this design exists to prevent.

// AuditPath is a project's trail — beside the documents, never under .raglit/.
func AuditPath(projectDir string) string {
	return filepath.Join(projectDir, "raglit-audit.jsonl")
}

// Audit op names. Values, not derived from Go identifiers: they are written to a
// file that outlives any refactor, and a rename must not orphan history.
const (
	OpRelationPut = "relation.put"
	OpSlicePut    = "slice.put"
	OpSliceDelete = "slice.delete"
	OpPageCorrect = "page.correct"
)

// AuditEvent is one recorded mutation.
type AuditEvent struct {
	Op string `json:"op"`
	// At is when the event was appended, not when a person decided — the decision
	// date lives on the payload. Both matter and they are not the same: a ruling
	// made in July and imported in September is one of each.
	At string `json:"at"`
	By string `json:"by,omitempty"`

	Relation   *Mark           `json:"relation,omitempty"`
	Slice      *Slice          `json:"slice,omitempty"`
	SliceID    string          `json:"slice_id,omitempty"`
	Correction *PageCorrection `json:"correction,omitempty"`
}

// PageCorrection is what a person read off a page that the machine got wrong.
//
// Text replaces the machine read for that page when the transcription is
// rendered. Note records HOW it was established — the magnification, the source
// image — because a corrected identifier that cannot be re-checked is only a
// different unverified value.
type PageCorrection struct {
	Doc  string `json:"doc"`
	Page int    `json:"page"`
	Text string `json:"text"`
	Note string `json:"note,omitempty"`
	By   string `json:"by,omitempty"`
	At   string `json:"at,omitempty"`
}

// AppendAudit writes one event. The only writer of the trail.
//
// Sync before returning: an event that is in the page cache and not on disk is
// an event a power cut turns into a decision nobody can account for.
func AppendAudit(path string, ev AuditEvent) error {
	if ev.At == "" {
		ev.At = time.Now().UTC().Format(time.RFC3339)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// ReadAudit parses the whole trail, in order. A missing file is an empty trail,
// which is the normal state of a corpus nobody has ruled on.
//
// A malformed line is an ERROR and not a skip. The trail is the source of the
// database, so silently ignoring a line it cannot parse would rebuild a
// database that quietly disagrees with the record — the failure this whole
// arrangement is meant to make impossible.
func ReadAudit(path string) ([]AuditEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []AuditEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if ev.Op == "" {
			return nil, fmt.Errorf("%s:%d: event has no op", path, n)
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}
