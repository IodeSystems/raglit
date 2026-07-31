package raglit

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iodesystems/raglit/internal/jdb"
)

//go:embed sql/judgements.sql
var judgementsSchema string

// The judgement store: what a person decided, kept where it survives.
//
// Separate from the index, and the separation is load-bearing rather than
// tidiness. The index lives at ~/.raglit/indexes/<name>/, which is outside the
// one folder Syncthing replicates and is gitignored in every project that has a
// local .raglit/; a reindex rebuilds it from the corpus. Everything in here is
// the opposite: nothing in the bytes says two deeds are versions of one
// instrument, or that pages 3-6 of a scan are the record of survey, so none of
// it can be recomputed and all of it has to outlive a rebuild.
//
// It IS a database rather than a text file, with real tables, generated queries
// and constraints — a pair is unique, a page range cannot run backwards, and a
// relation kind is checked in one place instead of at every call site. The one
// thing a text file gave that a database does not is a readable history, so
// judgement_log pays that back explicitly: every write appends the row as
// written, and nothing deletes from it.

// JudgementStore holds a project's rulings.
//
// The database is a PROJECTION. Every mutation is appended to the audit trail
// first and applied second, so the trail is the record and this is what you get
// by replaying it — see audit.go for why that ordering is the design.
type JudgementStore struct {
	db    *sql.DB
	q     *jdb.Queries
	audit string
}

// JudgementsPath is where a project's rulings live: beside the documents, never
// under .raglit/.
func JudgementsPath(projectDir string) string {
	return filepath.Join(projectDir, "judgements.db")
}

// OpenJudgements opens (creating if needed) a project's judgement database and
// binds it to the audit trail that is its source.
//
// A database behind its trail is brought up to date here rather than left to
// drift: the log-first write order means a crash between append and apply is
// normal and expected, so catching up on open is the ordinary path, not repair.
func OpenJudgements(path, auditPath string) (*JudgementStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("raglit: open judgements: %w", err)
	}
	// WAL for the same reason the index uses it: a reader must not block the
	// writer when a long report is running while a ruling is recorded.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(judgementsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("raglit: judgements schema: %w", err)
	}
	js := &JudgementStore{db: db, q: jdb.New(db), audit: auditPath}
	if auditPath != "" {
		if err := js.catchUp(); err != nil {
			db.Close()
			return nil, err
		}
	}
	return js, nil
}

// catchUp replays any events the database has not applied.
//
// Compares counts rather than diffing state: an event count is what the trail
// knows about itself, and if the database has applied fewer than the trail
// holds, replaying all of them is both correct and idempotent — every apply is
// an upsert or a delete by key.
func (s *JudgementStore) catchUp() error {
	events, err := ReadAudit(s.audit)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	var applied int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM judgement_log`).Scan(&applied); err != nil {
		return err
	}
	if applied >= int64(len(events)) {
		return nil
	}
	return s.replay(events, false)
}

// Rebuild drops every projected row and replays the trail from nothing.
//
// The test that the trail really is the source: if this does not reproduce the
// database, then something was written to the database that the log never
// recorded, and the audit trail is decoration.
func (s *JudgementStore) Rebuild() (int, error) {
	events, err := ReadAudit(s.audit)
	if err != nil {
		return 0, err
	}
	if err := s.replay(events, true); err != nil {
		return 0, err
	}
	return len(events), nil
}

// replay applies events in order, optionally clearing the projection first.
func (s *JudgementStore) replay(events []AuditEvent, clear bool) error {
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if clear {
		for _, t := range []string{"doc_relations", "doc_slices", "judgement_log"} {
			if _, err := tx.Exec("DELETE FROM " + t); err != nil {
				return err
			}
		}
	}
	q := s.q.WithTx(tx)
	for i, ev := range events {
		if err := applyEvent(ctx, q, ev); err != nil {
			return fmt.Errorf("audit event %d (%s): %w", i+1, ev.Op, err)
		}
	}
	return tx.Commit()
}

// applyEvent projects one event into the tables. The ONLY place an event
// becomes a row, so the live path and a rebuild cannot diverge.
func applyEvent(ctx context.Context, q *jdb.Queries, ev AuditEvent) error {
	switch ev.Op {
	case OpRelationPut:
		if ev.Relation == nil {
			return fmt.Errorf("relation.put with no relation")
		}
		m := ev.Relation.Normalize()
		if err := q.UpsertRelation(ctx, jdb.UpsertRelationParams{
			A: m.A, B: m.B, Kind: string(m.Kind),
			Supersedes: nullString(m.Supersedes),
			Note:       m.Note, DecidedBy: m.By, DecidedAt: m.At,
			Relation: string(m.Relation), Coverage: m.Coverage,
		}); err != nil {
			return err
		}
		return logEvent(ctx, q, "relation", m.A+"\x00"+m.B, m, ev)

	case OpSlicePut:
		if ev.Slice == nil {
			return fmt.Errorf("slice.put with no slice")
		}
		sl := *ev.Slice
		if err := q.UpsertSlice(ctx, jdb.UpsertSliceParams{
			ID: sl.ID, Parent: sl.Parent,
			FromPage: int64(sl.From), ToPage: int64(sl.To),
			Title: sl.Title, Note: sl.Note,
			DecidedBy: sl.By, DecidedAt: sl.At,
		}); err != nil {
			return err
		}
		return logEvent(ctx, q, "slice", sl.ID, sl, ev)

	case OpSliceDelete:
		if ev.SliceID == "" {
			return fmt.Errorf("slice.delete with no id")
		}
		if err := q.DeleteSlice(ctx, ev.SliceID); err != nil {
			return err
		}
		return logEvent(ctx, q, "slice", ev.SliceID, map[string]string{"deleted": ev.SliceID}, ev)
	}
	// An unknown op is an error, not a skip: a newer raglit wrote something this
	// one does not understand, and quietly ignoring it builds a database that
	// disagrees with the record.
	return fmt.Errorf("unknown op %q — written by a newer raglit?", ev.Op)
}

func logEvent(ctx context.Context, q *jdb.Queries, kind, subject string, payload any, ev AuditEvent) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return q.AppendJudgementLog(ctx, jdb.AppendJudgementLogParams{
		Kind: kind, Subject: subject, Payload: string(b),
		DecidedBy: ev.By, DecidedAt: ev.At, LoggedAt: time.Now().UnixNano(),
	})
}

// record appends an event to the trail, then applies it. Log first — see audit.go.
func (s *JudgementStore) record(ev AuditEvent) error {
	if s.audit != "" {
		if err := AppendAudit(s.audit, ev); err != nil {
			return err
		}
	}
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := applyEvent(ctx, s.q.WithTx(tx), ev); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *JudgementStore) Close() error { return s.db.Close() }

// ── relations ──────────────────────────────────────────────────────────

// PutRelation records a ruling on a pair, normalizing the sides so the pair is
// one fact however it was given.
func (s *JudgementStore) PutRelation(m Mark) error {
	if err := m.check(); err != nil {
		return err
	}
	m = m.Normalize()
	return s.record(AuditEvent{Op: OpRelationPut, By: m.By, Relation: &m})
}

// Relation returns the ruling on a pair, in either order.
func (s *JudgementStore) Relation(a, b string) (Mark, bool, error) {
	if b < a {
		a, b = b, a
	}
	row, err := s.q.GetRelation(context.Background(), jdb.GetRelationParams{A: a, B: b})
	if errors.Is(err, sql.ErrNoRows) {
		return Mark{}, false, nil
	}
	if err != nil {
		return Mark{}, false, err
	}
	return markOf(row), true, nil
}

// Relations returns every ruling.
func (s *JudgementStore) Relations() ([]Mark, error) {
	rows, err := s.q.ListRelations(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]Mark, 0, len(rows))
	for _, r := range rows {
		out = append(out, markOf(r))
	}
	return out, nil
}

// RelationsFor returns every ruling involving one document.
func (s *JudgementStore) RelationsFor(doc string) ([]Mark, error) {
	rows, err := s.q.ListRelationsFor(context.Background(), jdb.ListRelationsForParams{A: doc, B: doc})
	if err != nil {
		return nil, err
	}
	out := make([]Mark, 0, len(rows))
	for _, r := range rows {
		out = append(out, markOf(r))
	}
	return out, nil
}

func markOf(r jdb.DocRelation) Mark {
	return Mark{
		A: r.A, B: r.B, Kind: MarkKind(r.Kind),
		Supersedes: r.Supersedes.String,
		Note:       r.Note, By: r.DecidedBy, At: r.DecidedAt,
		Relation: Relation(r.Relation), Coverage: r.Coverage,
	}
}

// ── slices ─────────────────────────────────────────────────────────────

// PutSlice records that a page range of a bundle is a document.
func (s *JudgementStore) PutSlice(sl Slice) error {
	if err := sl.validate(); err != nil {
		return err
	}
	return s.record(AuditEvent{Op: OpSlicePut, By: sl.By, Slice: &sl})
}

// Slice returns one declaration by id.
func (s *JudgementStore) Slice(id string) (Slice, bool, error) {
	row, err := s.q.GetSlice(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Slice{}, false, nil
	}
	if err != nil {
		return Slice{}, false, err
	}
	return sliceOf(row), true, nil
}

// Slices returns every declaration, parent then page order.
func (s *JudgementStore) Slices() ([]Slice, error) {
	rows, err := s.q.ListSlices(context.Background())
	if err != nil {
		return nil, err
	}
	return slicesOf(rows), nil
}

// SlicesOf returns one bundle's declarations, in page order.
func (s *JudgementStore) SlicesOf(parent string) ([]Slice, error) {
	rows, err := s.q.ListSlicesOf(context.Background(), parent)
	if err != nil {
		return nil, err
	}
	return slicesOf(rows), nil
}

// SliceParents lists the bundles that have any slice.
func (s *JudgementStore) SliceParents() ([]string, error) {
	return s.q.ListSliceParents(context.Background())
}

// DeleteSlice removes a declaration. The trail keeps it, so a rebuild replays
// the creation and then the deletion and lands in the same place.
func (s *JudgementStore) DeleteSlice(id string) error {
	return s.record(AuditEvent{Op: OpSliceDelete, SliceID: id})
}

func sliceOf(r jdb.DocSlice) Slice {
	return Slice{
		ID: r.ID, Parent: r.Parent,
		From: int(r.FromPage), To: int(r.ToPage),
		Title: r.Title, Note: r.Note, By: r.DecidedBy, At: r.DecidedAt,
	}
}

func slicesOf(rows []jdb.DocSlice) []Slice {
	out := make([]Slice, 0, len(rows))
	for _, r := range rows {
		out = append(out, sliceOf(r))
	}
	return out
}

// ── history ────────────────────────────────────────────────────────────

// History returns every ruling ever recorded on one subject, oldest first — the
// property an append-only text file gave for free and a table has to keep on
// purpose. "Why did this change?" must stay answerable.
func (s *JudgementStore) History(kind, subject string) ([]jdb.JudgementLog, error) {
	return s.q.ListJudgementHistory(context.Background(),
		jdb.ListJudgementHistoryParams{Kind: kind, Subject: subject})
}

// CoverageOf reports which of a bundle's pages belong to no slice, and where
// slices overlap. See the type for why overlap is reported and not refused.
func (s *JudgementStore) CoverageOf(parent string, pages int) (Coverage, error) {
	sl, err := s.SlicesOf(parent)
	if err != nil {
		return Coverage{}, err
	}
	return sliceCoverage(parent, pages, sl), nil
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
