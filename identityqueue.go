package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// The captioning queue: durable work, drained at the endpoint's real concurrency.
//
// Three facts shape this, and only the first is about raglit.
//
//   - Identity is one bounded model call per document, and a corpus is hundreds.
//     A sweep that lives inside a command dies with the terminal, and nothing
//     else can see what is left. Rows survive that, and survive the machine.
//   - The endpoint serves TWO requests at a time. Firing a document per
//     goroutine does not make the model faster; it makes every request queue
//     inside the server, where raglit cannot see it, cannot resume it, and is
//     competing with its own ingest pipeline for the same two slots.
//   - Everything around the call — claiming a row, reassembling the document's
//     text, writing the caption back — is database work measured in
//     milliseconds. Doing it on the goroutine holding a slot leaves the model
//     idle while sqlite works, which wastes the scarce half of the system to
//     save nothing.
//
// So: one loader, `Slots` callers, one committer. A slot is held for the model
// call and nothing else.

// IdentityJob is one queued captioning task.
type IdentityJob struct {
	ID         int64  `json:"id"`
	Path       string `json:"path"`
	State      string `json:"state"` // pending|running|done|skipped|error
	Force      bool   `json:"force,omitempty"`
	Error      string `json:"error,omitempty"`
	EnqueuedAt int64  `json:"enqueued_at,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// IdentityQueueStatus is the queue in one line: how much is left, how much is
// running, how much finished, and how much failed.
type IdentityQueueStatus struct {
	Pending int `json:"pending"`
	Running int `json:"running"`
	Done    int `json:"done"`
	// Skipped is documents there was nothing to caption for — a scanned page with
	// forty characters on it, an empty attachment. Its own state rather than a
	// failure: nothing went wrong, nothing will go better next time, and counting
	// it as failed puts a permanent red number on a corpus that is fine. Kept out
	// of the re-queue for the same reason.
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

// Empty reports whether there is nothing outstanding.
func (q IdentityQueueStatus) Empty() bool { return q.Pending == 0 && q.Running == 0 }

// EnqueueIdentity queues one document for captioning. Idempotent per path: an
// existing pending/running row is left alone (re-queueing a document already in
// flight buys a duplicate model call and nothing else), and a TERMINAL row —
// done, skipped or error — is reset to pending. Returns false when an in-flight
// job already covers it.
//
// Reviving a skipped row is deliberate and is the reason this takes a path at
// all: the bulk enqueue leaves skips alone (nothing to read is not work), but a
// document named explicitly, or a --force sweep, is somebody saying that has
// changed — which it does the moment a document is re-OCR'd.
func (s *Store) EnqueueIdentity(path string, force bool) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, fmt.Errorf("raglit: enqueue identity: empty path")
	}
	now := time.Now().UnixNano()
	res, err := s.db.Exec(
		`INSERT INTO identity_jobs(path, state, force, enqueued_at) VALUES(?, 'pending', ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   state       = 'pending',
		   force       = excluded.force,
		   error       = '',
		   enqueued_at = excluded.enqueued_at,
		   started_at  = 0,
		   finished_at = 0,
		   owner_pid   = 0
		 WHERE identity_jobs.state IN ('done','skipped','error')`,
		path, boolInt(force), now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// enqueueIdentityTx is EnqueueIdentity inside a caller's transaction — used by
// commitDoc so a new transcript and the caption it owes are one atomic fact.
func enqueueIdentityTx(ctx context.Context, tx dbExecer, path string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO identity_jobs(path, state, force, enqueued_at) VALUES(?, 'pending', 0, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   state='pending', error='', enqueued_at=excluded.enqueued_at,
		   started_at=0, finished_at=0, owner_pid=0
		 WHERE identity_jobs.state IN ('done','skipped','error')`,
		path, time.Now().UnixNano())
	return err
}

// EnqueueIdentityFor queues every given path, reporting how many rows it added
// or revived. Paths already in flight are skipped, not duplicated.
func (s *Store) EnqueueIdentityFor(paths []string, force bool) (int, error) {
	n := 0
	for _, p := range paths {
		queued, err := s.EnqueueIdentity(p, force)
		if err != nil {
			return n, err
		}
		if queued {
			n++
		}
	}
	return n, nil
}

// EnqueueMissingIdentities queues every document with no caption yet — or, with
// force, every document. Returns how many were queued.
func (s *Store) EnqueueMissingIdentities(force bool) (int, error) {
	var paths []string
	var err error
	if force {
		docs, derr := s.Documents()
		if derr != nil {
			return 0, derr
		}
		for _, d := range docs {
			paths = append(paths, d.Path)
		}
	} else if paths, err = s.captionableMissing(); err != nil {
		return 0, err
	}
	return s.EnqueueIdentityFor(paths, force)
}

// captionableMissing is DocumentsMissingIdentity minus the documents a previous
// sweep already established have nothing to caption. Re-queueing those is a
// guaranteed no-op that would nonetheless read as outstanding work forever.
// --force still reaches them, because "nothing to read" can stop being true when
// a document is re-OCR'd.
func (s *Store) captionableMissing() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT d.path FROM documents d
		  WHERE TRIM(d.gen_name) = ''
		    AND NOT EXISTS (SELECT 1 FROM identity_jobs j WHERE j.path = d.path AND j.state = 'skipped')
		  ORDER BY d.added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// claimNextIdentityJob takes the oldest pending row and marks it running.
//
// ONE statement, not a transaction, and that is load-bearing. A deferred
// transaction that SELECTs and then UPDATEs takes a read snapshot first, and
// upgrading it to a write fails with SQLITE_BUSY *immediately* if any other
// writer committed in between — busy_timeout does not apply to a snapshot
// upgrade, because waiting cannot help: the snapshot is already stale. With two
// writers in this worker alone (this claim, and the committer storing captions)
// that raced within seconds on a live index. An UPDATE ... RETURNING is a single
// write statement: it waits its turn like any other writer, and cannot see a
// snapshot that has moved under it.
//
// Returns nil when the queue is empty.
func (s *Store) claimNextIdentityJob() (*IdentityJob, error) {
	var j IdentityJob
	var force int
	now := time.Now().UnixNano()
	err := s.db.QueryRow(
		`UPDATE identity_jobs SET state='running', started_at=?, owner_pid=?
		  WHERE id = (SELECT id FROM identity_jobs WHERE state='pending' ORDER BY id LIMIT 1)
		 RETURNING id, path, force, enqueued_at`,
		// The claiming process is stamped for the same reason ingest stamps it: a
		// 'running' row outlives the process that owned it, and without the pid a
		// fresh worker cannot tell its own live work from a dead worker's leftovers.
		now, os.Getpid()).
		Scan(&j.ID, &j.Path, &force, &j.EnqueuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.State, j.Force, j.StartedAt = "running", force != 0, now
	return &j, nil
}

// finishIdentityJob records a job's outcome: done, skipped (with the reason a
// caption was not possible), or error.
func (s *Store) finishIdentityJob(id int64, err error) error {
	state, msg := "done", ""
	var short *ErrIdentityTooShort
	switch {
	case err == nil:
	case errors.As(err, &short):
		state, msg = "skipped", err.Error()
	default:
		state, msg = "error", err.Error()
	}
	_, e := s.db.Exec(`UPDATE identity_jobs SET state=?, error=?, finished_at=? WHERE id=?`,
		state, msg, time.Now().UnixNano(), id)
	return e
}

// ReclaimIdentityJobs returns rows left 'running' by a process that is gone to
// pending, and reports how many.
//
// Requeued rather than failed, which is the opposite of what ingest does with
// its orphans — and the difference is the work. An ingest job may have been
// killed BY its document (an OOM on a huge scan), so retrying it on every start
// is a crash loop. A caption is one bounded request on text already in the
// index: if it died, the process died, and redoing it costs one call.
func (s *Store) ReclaimIdentityJobs() (int, error) {
	rows, err := s.db.Query(`SELECT id, owner_pid FROM identity_jobs WHERE state='running'`)
	if err != nil {
		return 0, err
	}
	type orphan struct{ id, pid int64 }
	var orphans []orphan
	self := int64(os.Getpid())
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.pid); err != nil {
			rows.Close()
			return 0, err
		}
		if o.pid == self || processAlive(o.pid) {
			continue
		}
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, o := range orphans {
		if _, err := s.db.Exec(
			`UPDATE identity_jobs SET state='pending', started_at=0, owner_pid=0 WHERE id=? AND state='running'`,
			o.id); err != nil {
			return 0, err
		}
	}
	return len(orphans), nil
}

// IdentityQueue reports the queue's counts.
func (s *Store) IdentityQueue() (IdentityQueueStatus, error) {
	var q IdentityQueueStatus
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM identity_jobs GROUP BY state`)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return q, err
		}
		switch state {
		case "pending":
			q.Pending = n
		case "running":
			q.Running = n
		case "done":
			q.Done = n
		case "skipped":
			q.Skipped = n
		case "error":
			q.Failed = n
		}
	}
	return q, rows.Err()
}

// IdentityJobs lists the queue, newest first, optionally only one state.
func (s *Store) IdentityJobs(state string, limit int) ([]IdentityJob, error) {
	q := `SELECT id, path, state, force, error, enqueued_at, started_at, finished_at FROM identity_jobs`
	var args []any
	if state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityJob
	for rows.Next() {
		var j IdentityJob
		var force int
		if err := rows.Scan(&j.ID, &j.Path, &j.State, &force, &j.Error,
			&j.EnqueuedAt, &j.StartedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		j.Force = force != 0
		out = append(out, j)
	}
	return out, rows.Err()
}

// DefaultIdentitySlots is how many captions are in the model at once when
// nothing says otherwise.
//
// TWO, because that is what the endpoint serves concurrently. A third request
// does not run — it waits inside the server, where raglit cannot see it, cannot
// resume it, and cannot tell it apart from an ingest job's OCR call waiting for
// the same slot. Matching the number is what keeps the queue's depth honest.
const DefaultIdentitySlots = 2

// IdentityWorker drains the captioning queue.
//
// Shaped as a pipeline rather than as N self-contained workers, because the
// model is the scarce resource and everything else is sqlite: a loader claims
// rows and reassembles document text, `Slots` callers do NOTHING but the model
// call, and a committer writes the captions and closes the rows. A slot is held
// for the request and not a millisecond longer.
type IdentityWorker struct {
	Store *Store
	// Slots is how many model calls are in flight. 0 → DefaultIdentitySlots.
	Slots int
	// IdlePoll is how long Run waits when the queue is empty. Default 2s.
	IdlePoll time.Duration
	// OnDone, when set, is called for each finished job — for a CLI that wants
	// to print progress. Called from the committer, one at a time.
	OnDone func(job IdentityJob, id DocIdentity, err error)
}

// identityTask is one job in flight between the pipeline's stages.
type identityTask struct {
	job  IdentityJob
	text string
	id   DocIdentity
	err  error
}

// Drain works the queue until it is empty, returning how many jobs finished
// (successfully or not). This is the whole sweep for a CLI; Run is the daemon's
// version, which waits for more work instead of returning.
func (w *IdentityWorker) Drain(ctx context.Context) (int, error) {
	return w.run(ctx, false)
}

// Run drains forever, sleeping IdlePoll between empty polls, until ctx is done.
func (w *IdentityWorker) Run(ctx context.Context) {
	_, _ = w.run(ctx, true)
}

func (w *IdentityWorker) run(ctx context.Context, forever bool) (int, error) {
	if w.Store == nil {
		return 0, fmt.Errorf("raglit: identity worker has no store")
	}
	if w.Store.identifier == nil {
		return 0, ErrNoIdentifier
	}
	slots := w.Slots
	if slots <= 0 {
		slots = DefaultIdentitySlots
	}
	poll := w.IdlePoll
	if poll <= 0 {
		poll = 2 * time.Second
	}

	// Buffered by ONE. A claimed row reads as 'running' to everything else, so a
	// deep buffer would report work in flight that is sitting in memory — and on
	// a crash, that is what has to be reclaimed. One document prepared ahead is
	// enough that a slot never waits on sqlite for its next input.
	loaded := make(chan identityTask, 1)
	finished := make(chan identityTask, slots)

	var loadErr error
	go func() {
		defer close(loaded)
		for ctx.Err() == nil {
			job, err := w.Store.claimNextIdentityJob()
			if err != nil {
				loadErr = err
				return
			}
			if job == nil {
				if !forever {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(poll):
				}
				continue
			}
			t := identityTask{job: *job}
			// Everything a caption needs, read here rather than on a slot: the
			// document's own words (origin='' — a re-run must read the document,
			// not the last caption of it) and whether this job may replace what
			// is already recorded.
			cur, err := w.Store.DocumentIdentity(job.Path)
			switch {
			case err != nil:
				t.err = err
			case cur.ByPerson() || (!cur.Empty() && !job.Force):
				t.err = ErrIdentityKept
			default:
				// The reading in force, not the machine's first attempt at it:
				// a corrected page is what the document says. See IdentityText.
				text, derr := w.Store.IdentityText(ctx, job.Path)
				switch {
				case derr != nil:
					t.err = derr
				case contentChars(text) < identityMinTextChars:
					// Decided here rather than in the model call: it is a property
					// of the text, and a document with nothing in it should not
					// occupy a slot to be told so.
					t.err = &ErrIdentityTooShort{Chars: contentChars(text)}
				default:
					t.text = text
				}
			}
			select {
			case loaded <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < slots; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range loaded {
				if t.err == nil {
					t.id, t.err = w.Store.identifier.Identify(ctx, t.text)
					t.text = "" // done with it; do not carry a document through the queue
				}
				select {
				case finished <- t:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(finished) }()

	n := 0
	var firstErr error
	for t := range finished {
		// The write, off the slot. A caption that fails to store is a failed
		// job, not a lost one — the row keeps the reason.
		err := t.err
		if err == nil {
			err = w.Store.SetDocumentIdentity(ctx, t.job.Path, t.id)
		}
		if errors.Is(err, ErrIdentityKept) {
			// Not a failure: something already answers for this document. The row
			// closes done, so a re-run does not keep re-asking.
			err = nil
		}
		// A too-short document closes SKIPPED, carrying the reason — see
		// finishIdentityJob. It is passed through here rather than nil'd so the
		// row records why, and so a caller's OnDone can tell the two apart.
		if ferr := w.Store.finishIdentityJob(t.job.ID, err); ferr != nil && firstErr == nil {
			firstErr = ferr
		}
		if w.OnDone != nil {
			w.OnDone(t.job, t.id, err)
		}
		n++
	}
	if loadErr != nil && firstErr == nil {
		firstErr = loadErr
	}
	return n, firstErr
}
