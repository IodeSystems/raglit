package raglit

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The judgement store: what a PERSON decided about this corpus.
//
// Nothing here can be recomputed. raglit can measure that two documents share
// 97% of their text; it cannot decide whether that means one instrument scanned
// twice or one instrument amended. A person decides, and the decision has to be
// written down or it is done again next week.
//
// raglit-audit.jsonl IS the store. Not a log of it, not a backup of it — the
// thing itself, read at open and answered from memory.
//
// It was briefly a SQLite database projected from the trail, with a schema, a
// generated query layer, a rebuild path and a table that duplicated the trail
// verbatim. On the corpus that motivated all of it: twelve events, eleven rows,
// a 68 KB database projecting a 12 KB file. Parsing the file is faster than
// opening the database, the constraints were already enforced in Go before any
// write, and the indexes covered eleven rows. It bought nothing and cost a
// concept everybody had to learn.
//
// If the trail ever grows past what is comfortable to parse, cache it. Until a
// measurement says so, answering at runtime is both simpler and correct by
// construction — there is no projection to drift, catch up, or rebuild.
type JudgementStore struct {
	audit string

	marks       map[string]Mark           // by normalized pair key
	slices      map[string]Slice          // by id
	corrections map[string]PageCorrection // by doc\x00page
	authority   map[string]ReadingAuthority // by source sha256
	withdrawn   map[string]Withdrawal     // by path
	history     map[string][]AuditEvent   // by subject, in order

	// onChange fires when a page's ACTIVE reading changes. Registered by whoever
	// owns what a correction invalidates — the export on disk, the rows in the
	// index — because neither notices on its own.
	//
	// Not fired while replaying at open: those are decisions being READ, not
	// made, and a listener re-rendering every corrected page on every open would
	// be a loop with no reason to run.
	onChange  []func(TranscriptionChange)
	replaying bool
}

// OpenJudgements reads a project's trail. A missing trail is a corpus nobody has
// ruled on, which is the normal starting state.
//
// The projection is gone. This answers from the trail, in memory, and there is
// no second database to drift, catch up or rebuild — verdicts that DO want a
// queryable projection now land in the index itself, see attestations.go.
func OpenJudgements(auditPath string) (*JudgementStore, error) {
	s := &JudgementStore{
		audit:       auditPath,
		marks:       map[string]Mark{},
		slices:      map[string]Slice{},
		corrections: map[string]PageCorrection{},
		authority:   map[string]ReadingAuthority{},
		withdrawn:   map[string]Withdrawal{},
		history:     map[string][]AuditEvent{},
	}
	events, err := ReadAudit(auditPath)
	if err != nil {
		return nil, err
	}
	s.replaying = true
	for i, ev := range events {
		if err := s.apply(ev); err != nil {
			s.replaying = false
			return nil, fmt.Errorf("audit event %d (%s): %w", i+1, ev.Op, err)
		}
	}
	s.replaying = false
	return s, nil
}

// OnTranscriptionChange registers a listener for a page's active reading
// changing. Listeners run in registration order, synchronously, after the event
// is durable in the trail.
func (s *JudgementStore) OnTranscriptionChange(fn func(TranscriptionChange)) {
	if fn != nil {
		s.onChange = append(s.onChange, fn)
	}
}

// Close exists so callers need not care that there is nothing to close.
func (s *JudgementStore) Close() error { return nil }

func correctionKey(doc string, page int) string { return fmt.Sprintf("%s\x00%d", doc, page) }

// apply folds one event into the answers. The ONLY place an event becomes state,
// so a fresh read and a live write cannot diverge.
func (s *JudgementStore) apply(ev AuditEvent) error {
	switch ev.Op {
	case OpAuthorityPut:
		if ev.Authority == nil {
			return fmt.Errorf("authority.put with no authority")
		}
		a := *ev.Authority
		if a.At == "" {
			a.At = ev.At
		}
		if a.By == "" {
			a.By = ev.By
		}
		s.authority[a.Source] = a
	case OpRelationPut:
		if ev.Relation == nil {
			return fmt.Errorf("relation.put with no relation")
		}
		m := ev.Relation.Normalize()
		s.marks[pairKey(m.A, m.B)] = m
		s.remember("relation", pairKey(m.A, m.B), ev)
	case OpSlicePut:
		if ev.Slice == nil {
			return fmt.Errorf("slice.put with no slice")
		}
		s.slices[ev.Slice.ID] = *ev.Slice
		s.remember("slice", ev.Slice.ID, ev)
	case OpSliceDelete:
		if ev.SliceID == "" {
			return fmt.Errorf("slice.delete with no id")
		}
		delete(s.slices, ev.SliceID)
		s.remember("slice", ev.SliceID, ev)
	case OpPageCorrect:
		if ev.Correction == nil {
			return fmt.Errorf("page.correct with no correction")
		}
		c := *ev.Correction
		prev := s.corrections[correctionKey(c.Doc, c.Page)]
		s.corrections[correctionKey(c.Doc, c.Page)] = c
		s.remember("correction", correctionKey(c.Doc, c.Page), ev)
		if !s.replaying {
			superseded := c.Supersedes
			if superseded == "" {
				superseded = prev.Text
			}
			for _, fn := range s.onChange {
				fn(TranscriptionChange{Doc: c.Doc, Page: c.Page, Active: c, Superseded: superseded})
			}
		}
	case OpDocWithdraw:
		if ev.Withdrawal == nil || ev.Withdrawal.Path == "" {
			return fmt.Errorf("doc.withdraw with no path")
		}
		if strings.TrimSpace(ev.Withdrawal.Reason) == "" {
			// The reason IS the withdrawal. Without it this is a delete wearing a
			// decision's clothes, and the next reader gets a document that is
			// absent for no stated cause — which is the state this op exists to
			// prevent.
			return fmt.Errorf("doc.withdraw %s with no reason", ev.Withdrawal.Path)
		}
		s.withdrawn[ev.Withdrawal.Path] = *ev.Withdrawal
		s.remember("withdrawal", ev.Withdrawal.Path, ev)
	case OpDocRestore:
		if ev.RestorePath == "" {
			return fmt.Errorf("doc.restore with no path")
		}
		delete(s.withdrawn, ev.RestorePath)
		s.remember("withdrawal", ev.RestorePath, ev)
	default:
		// An op from a newer raglit. Refused rather than skipped: quietly ignoring
		// it answers questions from a corpus that is missing decisions somebody
		// recorded.
		return fmt.Errorf("unknown op %q — written by a newer raglit?", ev.Op)
	}
	return nil
}

func (s *JudgementStore) remember(kind, subject string, ev AuditEvent) {
	k := kind + "\x00" + subject
	s.history[k] = append(s.history[k], ev)
}

// record appends to the trail, then folds it in. Append first: a crash between
// the two loses nothing, because the next open replays the trail. Applying first
// would leave a decision in memory that the record never received.
func (s *JudgementStore) record(ev AuditEvent) error {
	if s.audit != "" {
		if err := AppendAudit(s.audit, ev); err != nil {
			return err
		}
	}
	return s.apply(ev)
}

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

// PutAuthority records which reading of a source governs.
//
// Keyed on the SOURCE, not on a pair: a third reading arriving later is ordered
// against the others by this one ruling rather than needing a new one for every
// combination. Re-ruling replaces — a person changing their mind is one fact,
// and the trail keeps both.
func (s *JudgementStore) PutAuthority(a ReadingAuthority) error {
	if a.Source == "" || a.Doc == "" {
		return fmt.Errorf("raglit: an authority ruling needs a source and the reading that governs it")
	}
	return s.record(AuditEvent{Op: OpAuthorityPut, By: a.By, Authority: &a})
}

// Authority returns the ruling for a source, if a person has made one.
func (s *JudgementStore) Authority(source string) (ReadingAuthority, bool) {
	a, ok := s.authority[source]
	return a, ok
}

// Authorities returns every authority ruling.
func (s *JudgementStore) Authorities() []ReadingAuthority {
	out := make([]ReadingAuthority, 0, len(s.authority))
	for _, a := range s.authority {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// Relation returns the ruling on a pair, in either order.
func (s *JudgementStore) Relation(a, b string) (Mark, bool, error) {
	m, ok := s.marks[pairKey(a, b)]
	return m, ok, nil
}

// Relations returns every ruling, in a stable order.
func (s *JudgementStore) Relations() ([]Mark, error) {
	out := make([]Mark, 0, len(s.marks))
	for _, m := range s.marks {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, nil
}

// RelationsFor returns every ruling involving one document.
func (s *JudgementStore) RelationsFor(doc string) ([]Mark, error) {
	all, _ := s.Relations()
	out := all[:0:0]
	for _, m := range all {
		if _, ok := m.Other(doc); ok {
			out = append(out, m)
		}
	}
	return out, nil
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
	sl, ok := s.slices[id]
	return sl, ok, nil
}

// Slices returns every declaration, parent then page order.
func (s *JudgementStore) Slices() ([]Slice, error) {
	out := make([]Slice, 0, len(s.slices))
	for _, sl := range s.slices {
		out = append(out, sl)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Parent != out[j].Parent {
			return out[i].Parent < out[j].Parent
		}
		return out[i].From < out[j].From
	})
	return out, nil
}

// SlicesOf returns one bundle's declarations, in page order.
func (s *JudgementStore) SlicesOf(parent string) ([]Slice, error) {
	all, _ := s.Slices()
	out := all[:0:0]
	for _, sl := range all {
		if sl.Parent == parent {
			out = append(out, sl)
		}
	}
	return out, nil
}

// SliceParents lists the bundles that have any slice.
func (s *JudgementStore) SliceParents() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, sl := range s.slices {
		if !seen[sl.Parent] {
			seen[sl.Parent] = true
			out = append(out, sl.Parent)
		}
	}
	sort.Strings(out)
	return out, nil
}

// DeleteSlice removes a declaration. The trail keeps the creation AND the
// deletion, so replaying lands in the same place and the history stays readable.
func (s *JudgementStore) DeleteSlice(id string) error {
	return s.record(AuditEvent{Op: OpSliceDelete, SliceID: id})
}

// ── page corrections ───────────────────────────────────────────────────

// PutPageCorrection records what a person read off a page.
//
// It carries what it SUPERSEDES. A correction attests that a better reading
// exists; it does not erase the old one, which stays in the trail as the reading
// the index held when the document was cited.
func (s *JudgementStore) PutPageCorrection(c PageCorrection) error {
	if c.Doc == "" || c.Page < 1 {
		return fmt.Errorf("a correction needs a document and a page number")
	}
	if strings.TrimSpace(c.Text) == "" {
		return fmt.Errorf("a correction needs the corrected text")
	}
	if c.Supersedes == "" {
		if prev, ok := s.corrections[correctionKey(c.Doc, c.Page)]; ok {
			c.Supersedes = prev.Text
		}
	}
	return s.record(AuditEvent{Op: OpPageCorrect, By: c.By, Correction: &c})
}

// PageCorrections returns the ACTIVE correction for each page of a document.
func (s *JudgementStore) PageCorrections(doc string) (map[int]PageCorrection, error) {
	out := map[int]PageCorrection{}
	for _, c := range s.corrections {
		if c.Doc == doc {
			out[c.Page] = c
		}
	}
	return out, nil
}

// PageReadings returns every reading ever recorded for one page, oldest first.
// The last is the active one; the rest are superseded and kept.
func (s *JudgementStore) PageReadings(doc string, page int) []PageCorrection {
	var out []PageCorrection
	for _, ev := range s.history["correction\x00"+correctionKey(doc, page)] {
		if ev.Correction != nil {
			out = append(out, *ev.Correction)
		}
	}
	return out
}

// ── history ────────────────────────────────────────────────────────────

// History returns every event recorded on one subject, oldest first.
func (s *JudgementStore) History(kind, subject string) ([]AuditEvent, error) {
	return s.history[kind+"\x00"+subject], nil
}

// CoverageOf reports which of a bundle's pages belong to no slice.
func (s *JudgementStore) CoverageOf(parent string, pages int) (Coverage, error) {
	sl, err := s.SlicesOf(parent)
	if err != nil {
		return Coverage{}, err
	}
	return sliceCoverage(parent, pages, sl), nil
}

// JudgementsPath is retained so callers compile unchanged. The trail is the
// store; there is no database.
// JudgementsPath is retained as an alias for AuditPath.
//
// Deprecated: there is no judgements database. The name outlived the file it
// named, and callers asking "where are the rulings" want the trail.
func JudgementsPath(projectDir string) string { return AuditPath(projectDir) }

var _ = time.Now

// Withdrawn reports the ruling that took a document out of the corpus, if any.
func (s *JudgementStore) Withdrawn(path string) (Withdrawal, bool) {
	if s == nil {
		return Withdrawal{}, false
	}
	w, ok := s.withdrawn[path]
	return w, ok
}

// Withdrawals lists every document ruled out, path order.
func (s *JudgementStore) Withdrawals() []Withdrawal {
	if s == nil {
		return nil
	}
	out := make([]Withdrawal, 0, len(s.withdrawn))
	for _, w := range s.withdrawn {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Withdraw records that a document is out of the corpus, and why.
//
// Appends first, then applies — the order audit.go explains: a process that dies
// between leaves the trail ahead of the state, which a re-read fixes, where the
// reverse would leave a decision in memory that nothing recorded.
func (s *JudgementStore) Withdraw(w Withdrawal) error {
	if strings.TrimSpace(w.Reason) == "" {
		return fmt.Errorf("raglit: withdraw %s: a withdrawal needs a reason", w.Path)
	}
	ev := AuditEvent{Op: OpDocWithdraw, At: time.Now().UTC().Format(time.RFC3339), By: w.By, Withdrawal: &w}
	if err := AppendAudit(s.audit, ev); err != nil {
		return err
	}
	return s.apply(ev)
}

// Restore returns a withdrawn document to the corpus. The withdrawal stays in
// the trail: a decision reversed is still a decision that was made.
func (s *JudgementStore) Restore(path, by string) error {
	ev := AuditEvent{Op: OpDocRestore, At: time.Now().UTC().Format(time.RFC3339), By: by, RestorePath: path}
	if err := AppendAudit(s.audit, ev); err != nil {
		return err
	}
	return s.apply(ev)
}
