package raglit

import (
	"context"
	"database/sql"
	"fmt"
)

// Recording every version of what a page says.
//
// Raw SQL rather than the generated layer, matching the precedent set for FTS5
// and the vector scan: this table was added after the queries were generated and
// the four statements here are simpler than a regeneration cycle.

// PageReading is one version of a page's text.
type PageReading struct {
	Doc    string `json:"doc"`
	Page   int    `json:"page"`
	Seq    int    `json:"seq"`
	Text   string `json:"text"`
	Source string `json:"source"` // machine | corrected
	Note   string `json:"note,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
	Active bool   `json:"active"`
}

// AddPageReading appends a reading and makes it the active one.
//
// Appends rather than replaces. The previous reading stays, marked inactive, so
// "what did this page say before somebody checked it" remains answerable — which
// is the whole reason a correction is an attestation rather than an edit.
func (s *Store) AddPageReading(ctx context.Context, r PageReading) error {
	if r.Doc == "" || r.Page < 1 {
		return fmt.Errorf("raglit: a page reading needs a document and a page")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var next sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM page_readings WHERE doc = ? AND page = ?`, r.Doc, r.Page).Scan(&next); err != nil {
		return err
	}
	seq := int(next.Int64) + 1

	// Identical text is not a new version. Re-ingesting a document nobody
	// corrected would otherwise grow a row per read forever.
	var same int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM page_readings WHERE doc = ? AND page = ? AND seq = ? AND text = ?`,
		r.Doc, r.Page, seq-1, r.Text).Scan(&same); err != nil {
		return err
	}
	if same > 0 {
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE page_readings SET active = 0 WHERE doc = ? AND page = ?`, r.Doc, r.Page); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO page_readings(doc, page, seq, text, source, note, read_by, read_at, active)
		 VALUES(?,?,?,?,?,?,?,?,1)`,
		r.Doc, r.Page, seq, r.Text, r.Source, r.Note, r.By, r.At); err != nil {
		return err
	}
	return tx.Commit()
}

// PageReadings returns every recorded version of one page, oldest first.
func (s *Store) PageReadings(ctx context.Context, doc string, page int) ([]PageReading, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT doc, page, seq, text, source, note, read_by, read_at, active
		   FROM page_readings WHERE doc = ? AND page = ? ORDER BY seq`, doc, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageReading
	for rows.Next() {
		var r PageReading
		var active int
		if err := rows.Scan(&r.Doc, &r.Page, &r.Seq, &r.Text, &r.Source, &r.Note, &r.By, &r.At, &active); err != nil {
			return nil, err
		}
		r.Active = active == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// SupersededPages lists documents holding a page whose reading was replaced —
// the corpus's record of where a machine read was found wanting.
func (s *Store) SupersededPages(ctx context.Context) ([]PageReading, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT doc, page, seq, text, source, note, read_by, read_at, active
		   FROM page_readings WHERE active = 0 ORDER BY doc, page, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageReading
	for rows.Next() {
		var r PageReading
		var active int
		if err := rows.Scan(&r.Doc, &r.Page, &r.Seq, &r.Text, &r.Source, &r.Note, &r.By, &r.At, &active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordReadingsInto wires a judgement store to an index so that every change to
// a page's ACTIVE reading lands as a row.
//
// This is the hook the interface exposes, used for its intended purpose: a
// correction invalidates things it does not own — the export beside the document
// and the fragments in the index — and neither notices on its own. Registering
// here means a person correcting a page leaves a visible, queryable version
// history rather than a silently changed file.
func RecordReadingsInto(js Judgements, s *Store) {
	js.OnTranscriptionChange(func(c TranscriptionChange) {
		ctx := context.Background()
		// The reading being replaced is recorded first when it is not already
		// there, so the row history starts at what the machine said rather than
		// at the first time somebody disagreed with it.
		if c.Superseded != "" {
			if prev, err := s.PageReadings(ctx, c.Doc, c.Page); err == nil && len(prev) == 0 {
				_ = s.AddPageReading(ctx, PageReading{
					Doc: c.Doc, Page: c.Page, Text: c.Superseded, Source: "machine",
				})
			}
		}
		_ = s.AddPageReading(ctx, PageReading{
			Doc: c.Doc, Page: c.Page, Text: c.Active.Text, Source: "corrected",
			Note: c.Active.Note, By: c.Active.By, At: c.Active.At,
		})
	})
}

// DocReadingHistory returns every recorded reading of every page of one
// document, in page then arrival order.
//
// Every version, not the active one. The whole reason readings accumulate as
// rows is that a correction must not erase what it replaced: the superseded text
// is what the index held when the document was cited, it is what a stale
// quotation elsewhere still matches, and "the OCR read 2008081020 for
// 200808180120" is itself evidence of how far a sheet can be trusted. A history
// that shows only the current answer throws away the thing the table exists for.
func (s *Store) DocReadingHistory(doc string) ([]PageReading, error) {
	rows, err := s.db.Query(`SELECT doc, page, seq, text, source, note, read_by, read_at, active
		FROM page_readings WHERE doc = ? ORDER BY page, seq`, doc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PageReading
	for rows.Next() {
		var r PageReading
		var active int
		if err := rows.Scan(&r.Doc, &r.Page, &r.Seq, &r.Text, &r.Source, &r.Note, &r.By, &r.At, &active); err != nil {
			return nil, err
		}
		r.Active = active != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
