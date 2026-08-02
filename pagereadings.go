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
	// Engine and Model say WHICH reader produced this, where Source says only
	// whether it was a machine at all. A corpus read by a text layer, then
	// tesseract, then one vision model, then another cannot answer "how far do I
	// trust this page" while all four are equally "machine".
	Engine string `json:"engine,omitempty"` // vision | tesseract | paddleocr | text-layer
	Model  string `json:"model,omitempty"`  // the model id behind the engine, when it has one
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

	// A machine NEVER unseats a person.
	//
	// Re-reading is routine — a new engine, a better model, a --fresh ingest —
	// and every one of those arrives claiming to be the latest word on the page.
	// If the newest reading always won, the first re-OCR after a correction would
	// silently put the machine's text back in force, and the correction would
	// survive only as history. So a machine reading lands INACTIVE when a person's
	// correction holds the page: recorded in full, available to compare, not in
	// force. A person's reading always takes the seat.
	active := 1
	if r.Source != "corrected" {
		var heldByPerson int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM page_readings WHERE doc = ? AND page = ? AND active = 1 AND source = 'corrected'`,
			r.Doc, r.Page).Scan(&heldByPerson); err != nil {
			return err
		}
		if heldByPerson > 0 {
			active = 0
		}
	}
	if active == 1 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE page_readings SET active = 0 WHERE doc = ? AND page = ?`, r.Doc, r.Page); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO page_readings(doc, page, seq, text, source, engine, model, note, read_by, read_at, active)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		r.Doc, r.Page, seq, r.Text, r.Source, r.Engine, r.Model, r.Note, r.By, r.At, active); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// A correction changes what the document SAYS, so the caption written from
	// the old reading is owed again — the same edge a re-read fires, from the
	// other direction. The document's own caption is left alone when a person
	// wrote it; IdentifyDocument refuses those anyway, and queueing work that is
	// certain to be declined is just noise in the queue.
	if active == 1 && r.Source == "corrected" && s.identifier != nil {
		if cur, err := s.DocumentIdentity(r.Doc); err == nil && !cur.ByPerson() && !cur.Empty() {
			_, _ = s.EnqueueIdentity(r.Doc, true)
		}
	}
	return nil
}

// PageReadings returns every recorded version of one page, oldest first.
func (s *Store) PageReadings(ctx context.Context, doc string, page int) ([]PageReading, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT doc, page, seq, text, source, engine, model, note, read_by, read_at, active
		   FROM page_readings WHERE doc = ? AND page = ? ORDER BY seq`, doc, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageReading
	for rows.Next() {
		var r PageReading
		var active int
		if err := rows.Scan(&r.Doc, &r.Page, &r.Seq, &r.Text, &r.Source, &r.Engine, &r.Model, &r.Note, &r.By, &r.At, &active); err != nil {
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
		`SELECT doc, page, seq, text, source, engine, model, note, read_by, read_at, active
		   FROM page_readings WHERE active = 0 ORDER BY doc, page, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageReading
	for rows.Next() {
		var r PageReading
		var active int
		if err := rows.Scan(&r.Doc, &r.Page, &r.Seq, &r.Text, &r.Source, &r.Engine, &r.Model, &r.Note, &r.By, &r.At, &active); err != nil {
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
					Engine: s.engineForPage(c.Doc, c.Page),
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
	rows, err := s.db.Query(`SELECT doc, page, seq, text, source, engine, model, note, read_by, read_at, active
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

// ActiveReadings returns the reading IN FORCE for each page of a document, by
// page number. Pages nobody has ruled on are absent — a document with no
// corrections returns an empty map, which is the common case and costs one
// indexed lookup.
func (s *Store) ActiveReadings(ctx context.Context, doc string) (map[int]PageReading, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT page, seq, text, source, engine, model, note, read_by, read_at
		   FROM page_readings WHERE doc = ? AND active = 1`, doc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]PageReading{}
	for rows.Next() {
		r := PageReading{Doc: doc, Active: true}
		if err := rows.Scan(&r.Page, &r.Seq, &r.Text, &r.Source, &r.Engine, &r.Model, &r.Note, &r.By, &r.At); err != nil {
			return nil, err
		}
		out[r.Page] = r
	}
	return out, rows.Err()
}

// engineForPage names the reader that produced a document's page, from the
// provenance ingest recorded. Empty when the page predates that record — which
// is the honest answer, not a guess.
func (s *Store) engineForPage(doc string, page int) string {
	var engine string
	err := s.db.QueryRow(
		`SELECT o.engine FROM ocr_pages o JOIN documents d ON d.id = o.doc_id
		  WHERE d.path = ? AND o.page = ?`, doc, page).Scan(&engine)
	if err != nil {
		return ""
	}
	return engine
}
