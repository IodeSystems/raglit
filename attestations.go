package raglit

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/iodesystems/raglit/attest"
)

// Verdicts, as rows.
//
// The jsonl beside the asset is the RECORD; this is the queryable view of it.
// That order matters and is the same one page_readings follows: a ruling cannot
// be recomputed, so it has to survive a reindex and reach the other machines,
// and a database that can be rebuilt from a log is safe to delete while a log
// projected from a database is not.
//
// What this replaces is judgements.db — a second sqlite file beside the corpus
// holding a projection of the same audit trail. One projection, in the index
// that already holds the documents the verdicts are about, so "which documents
// are reviewed" is a join rather than a cross-file reconciliation.

// AttestationRow is one entry of one asset's log, as stored.
type AttestationRow struct {
	Doc       string
	Seq       int
	Unit      string
	Kind      string
	Blanket   bool
	Text      string
	Label     string
	Note      string
	Statement string
	Payload   string
	RuledBy   string
	Auth      string
	RuledAt   string
}

// PutAttestations replaces this document's projected rows with the log as it
// now stands.
//
// Delete-then-insert rather than an incremental append, because the log is
// authoritative and append-only: re-projecting the whole of it is idempotent and
// cannot drift, while tracking a high-water mark can silently miss an entry a
// second machine appended between syncs. The logs are small — one asset's
// rulings — so the cost is not worth the class of bug.
func (s *Store) PutAttestations(doc string, entries []attest.Entry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM attestations WHERE doc = ?`, doc); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO attestations
		(doc, seq, unit, kind, blanket, text, label, note, statement, payload, ruled_by, auth, ruled_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for i, e := range entries {
		payload := ""
		// Resegment carries structure rather than a ruling. Kept as JSON: it is
		// replayed by attest.Resolve and never queried field by field here, so
		// giving it columns would be schema nobody reads.
		if len(e.Units) > 0 || len(e.Supersedes) > 0 {
			b, err := json.Marshal(struct {
				Units      []attest.Unit `json:"units,omitempty"`
				Supersedes []string      `json:"supersedes,omitempty"`
			}{e.Units, e.Supersedes})
			if err != nil {
				return err
			}
			payload = string(b)
		}
		if _, err := stmt.Exec(doc, i+1, e.Unit, string(e.Kind), boolInt(e.Blanket),
			e.Text, e.Label, e.Note, e.Statement, payload, e.By, e.Auth, e.At); err != nil {
			return fmt.Errorf("attestation %d of %s: %w", i+1, doc, err)
		}
	}
	return tx.Commit()
}

// Attestations returns one document's rows in log order.
func (s *Store) Attestations(doc string) ([]AttestationRow, error) {
	rows, err := s.db.Query(`SELECT doc, seq, unit, kind, blanket, text, label, note,
		statement, payload, ruled_by, auth, ruled_at
		FROM attestations WHERE doc = ? ORDER BY seq`, doc)
	if err != nil {
		return nil, err
	}
	return scanAttestations(rows)
}

// AttestedDocs reports how many entries each document has, for a review index
// that can say what has been looked at without opening every log.
//
// Deliberately NOT a coverage percentage. How much of a document is reviewed is
// a question about the CURRENT reading, and this table holds verdicts against
// units that may no longer exist — see the schema note on orphans. A count of
// rulings presented as coverage would report a document as done after a re-read
// invalidated every one of them.
func (s *Store) AttestedDocs() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT doc, COUNT(*) FROM attestations GROUP BY doc`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var doc string
		var n int
		if err := rows.Scan(&doc, &n); err != nil {
			return nil, err
		}
		out[doc] = n
	}
	return out, rows.Err()
}

// AttestationsForUnit returns every ruling on one unit, in log order.
//
// Every ruling, not the one in force: a retracted confirmation and the retraction
// are both facts about what happened, and which one governs is attest.Resolve's
// answer rather than SQL's.
func (s *Store) AttestationsForUnit(doc, unit string) ([]AttestationRow, error) {
	rows, err := s.db.Query(`SELECT doc, seq, unit, kind, blanket, text, label, note,
		statement, payload, ruled_by, auth, ruled_at
		FROM attestations WHERE doc = ? AND unit = ? ORDER BY seq`, doc, unit)
	if err != nil {
		return nil, err
	}
	return scanAttestations(rows)
}

func scanAttestations(rows *sql.Rows) ([]AttestationRow, error) {
	defer func() { _ = rows.Close() }()
	var out []AttestationRow
	for rows.Next() {
		var r AttestationRow
		var blanket int
		if err := rows.Scan(&r.Doc, &r.Seq, &r.Unit, &r.Kind, &blanket, &r.Text, &r.Label,
			&r.Note, &r.Statement, &r.Payload, &r.RuledBy, &r.Auth, &r.RuledAt); err != nil {
			return nil, err
		}
		r.Blanket = blanket != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
