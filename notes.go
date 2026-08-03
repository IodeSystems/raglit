package raglit

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Notes: what a person knows about a document that no machine could read off it.
//
// The case this was built for is ordinary and was unfixable: a document had been
// auto-titled as if it were the survey, when it is somebody's annotation OF the
// survey. The machine read the sheet and described the sheet, and the thing that
// made the document worth keeping went unmentioned. A re-title fixes the name.
// A note is for everything that is not a name — why this copy matters, which
// exhibit starts where, who to ask.
//
// See sql/schema.sql for why this is its own table rather than an attestation or
// an identity, and why a note outlives a re-ingest.

// Note is one person's comment on a document, or on one of its pages.
type Note struct {
	ID int64 `json:"id"`
	// Page is 0 for a note about the whole document. Stored as NULL in that
	// case — the column is nullable so "about the document" and "about page 0"
	// cannot be confused, and page 0 is a real value here (text windows carry
	// it).
	Page      int    `json:"page,omitempty"`
	Body      string `json:"body"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Notes returns every note on a document, oldest first.
//
// Oldest first because a note thread is read as it was written: the later note
// usually answers the earlier one, and reversing that makes a correction arrive
// before the thing it corrects.
func (s *Store) Notes(path string) ([]Note, error) {
	docID, err := s.docIDForNote(path)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT id, page, body, author, created_at
		   FROM document_notes WHERE doc_id = ? ORDER BY id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Note
	for rows.Next() {
		var n Note
		var page sql.NullInt64
		if err := rows.Scan(&n.ID, &page, &n.Body, &n.Author, &n.CreatedAt); err != nil {
			return nil, err
		}
		if page.Valid {
			n.Page = int(page.Int64)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddNote records a note. page <= 0 files it against the whole document.
//
// An empty body is refused rather than stored. A blank note is not a state
// anybody meant to reach — it is a stray click — and one sitting in a thread
// costs every later reader the moment it takes to work out that it says nothing.
func (s *Store) AddNote(path string, n Note) (Note, error) {
	n.Body = strings.TrimSpace(n.Body)
	if n.Body == "" {
		return Note{}, errors.New("raglit: a note needs a body")
	}
	n.Author = strings.TrimSpace(n.Author)
	docID, err := s.docIDForNote(path)
	if err != nil {
		return Note{}, err
	}
	if n.CreatedAt == "" {
		n.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	var page any
	if n.Page > 0 {
		page = n.Page
	}
	res, err := s.db.Exec(
		`INSERT INTO document_notes(doc_id, page, body, author, created_at) VALUES(?,?,?,?,?)`,
		docID, page, n.Body, n.Author, n.CreatedAt)
	if err != nil {
		return Note{}, err
	}
	n.ID, err = res.LastInsertId()
	return n, err
}

// DeleteNote removes one note by id.
//
// Hard delete, not a tombstone. A note is somebody's comment, not a ruling
// anything downstream replays, so there is nothing that has to be able to see
// that it once existed — which is the only reason attestations accumulate
// instead.
func (s *Store) DeleteNote(id int64) error {
	_, err := s.db.Exec(`DELETE FROM document_notes WHERE id = ?`, id)
	return err
}

// docIDForNote resolves a path to its row id.
//
// An unknown path is an error rather than an empty list. A note filed against a
// document that is not in the index would be unreachable from every screen that
// shows notes, so accepting it would be a write that silently goes nowhere.
func (s *Store) docIDForNote(path string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM documents WHERE path = ?`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("raglit: no document with path %q", path)
	}
	return id, err
}
