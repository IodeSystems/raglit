package raglit

import (
	"context"
	"fmt"
	"strings"
)

// Withdrawing a document from an index.
//
// Deleting a document and withdrawing one are different acts. A delete removes
// rows; the file is still on disk, so the next sweep re-ingests it and the
// decision lasts until the next change. A withdrawal is a RULING — it carries
// grounds, it is recorded in raglit-audit.jsonl where git can blame it, and the
// ingest path honours it, so the document stays out.
//
// Written for a corpus of legal evidence that had absorbed a drafts folder:
// outreach letters, settlement proposals, argument written by the party. A
// search for a phrase from the party's own draft returned it looking like a
// source. A draft is argument, not record — only a letter actually sent and
// declared is evidence of anything.

// Withdraw removes a document from this index and records why.
//
// Idempotent: withdrawing an already-withdrawn path refreshes the grounds and
// removes any rows a re-ingest slipped in before the decision was made.
func (s *Store) Withdraw(w Withdrawal) error {
	if strings.TrimSpace(w.Path) == "" {
		return fmt.Errorf("raglit: withdraw: no path")
	}
	if strings.TrimSpace(w.Reason) == "" {
		return fmt.Errorf("raglit: withdraw %s: a withdrawal needs a reason", w.Path)
	}
	if _, err := s.db.Exec(
		`INSERT INTO withdrawals (path, reason, by_who, at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET reason=excluded.reason, by_who=excluded.by_who, at=excluded.at`,
		w.Path, w.Reason, w.By, w.At); err != nil {
		return fmt.Errorf("raglit: withdraw %s: %w", w.Path, err)
	}
	// The rows go last: the ruling is recorded even if the delete fails, which is
	// the safe order — a withdrawn document still present is visible and fixable,
	// a deleted document with no ruling is neither.
	if err := s.DeleteDocument(w.Path); err != nil {
		return fmt.Errorf("raglit: withdraw %s: removing rows: %w", w.Path, err)
	}
	return nil
}

// Restore returns a withdrawn document to the corpus. It does not re-ingest —
// the file has to be indexed again, which is the caller's next step.
func (s *Store) Restore(path string) error {
	_, err := s.db.Exec(`DELETE FROM withdrawals WHERE path = ?`, path)
	return err
}

// WithdrawnReason reports why a path is out of the corpus, if it is.
func (s *Store) WithdrawnReason(path string) (string, bool) {
	var reason string
	if err := s.db.QueryRow(`SELECT reason FROM withdrawals WHERE path = ?`, path).Scan(&reason); err != nil {
		return "", false
	}
	return reason, true
}

// Withdrawals lists every ruling, path order.
func (s *Store) Withdrawals() ([]Withdrawal, error) {
	rows, err := s.db.Query(`SELECT path, reason, by_who, at FROM withdrawals ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Withdrawal
	for rows.Next() {
		var w Withdrawal
		if err := rows.Scan(&w.Path, &w.Reason, &w.By, &w.At); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ReferencesTo finds fragments that CITE a path, so a withdrawal does not leave
// live references pointing at a document nobody can retrieve.
//
// Matched on the path and on its file name, because a citation in this corpus is
// written either way — "documents/drafts/x.md" in a packet, "x.md" in a note.
// Fragments of the withdrawn document itself are excluded: a document citing its
// own name is not a dangling reference.
func (s *Store) ReferencesTo(ctx context.Context, path string) ([]Reference, error) {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.path, f.page, f.text FROM fragments f
		   JOIN documents d ON d.id = f.doc_id
		  WHERE (f.text LIKE ? OR f.text LIKE ?) AND d.path <> ? AND `+SQLOwnWordsF+`
		  ORDER BY d.path, f.page`,
		"%"+path+"%", "%"+base+"%", path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reference
	for rows.Next() {
		var r Reference
		var text string
		if err := rows.Scan(&r.From, &r.Page, &text); err != nil {
			return nil, err
		}
		r.Excerpt = referenceExcerpt(text, base)
		r.To = path
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reference is one fragment citing another document.
type Reference struct {
	From    string `json:"from"`
	Page    int    `json:"page,omitempty"`
	To      string `json:"to"`
	Excerpt string `json:"excerpt"`
}

// referenceExcerpt is the sentence around a citation, so a report shows the
// claim the reference supports rather than a file name in isolation.
func referenceExcerpt(text, needle string) string {
	i := strings.Index(text, needle)
	if i < 0 {
		return firstChars(text, 160)
	}
	start := i - 80
	if start < 0 {
		start = 0
	}
	end := i + len(needle) + 80
	if end > len(text) {
		end = len(text)
	}
	return strings.TrimSpace(text[start:end])
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
