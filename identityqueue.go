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
	ID    int64  `json:"id"`
	Path  string `json:"path"`
	State string `json:"state"` // pending|running|done|skipped|error
	Force bool   `json:"force,omitempty"`
	// TagsOnly asks for TAGS and leaves the caption alone — the backfill for a
	// corpus captioned before tags existed. See Store.TagDocument.
	TagsOnly   bool   `json:"tags_only,omitempty"`
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
	return s.enqueueIdentity(path, force, false)
}

// EnqueueTags queues one document for the TAGS-ONLY ask. Same queue, same
// rows, same resumability — a backfill of hundreds is the same shape of work
// as a captioning sweep and has no business being a second mechanism.
func (s *Store) EnqueueTags(path string, force bool) (bool, error) {
	return s.enqueueIdentity(path, force, true)
}

func (s *Store) enqueueIdentity(path string, force, tagsOnly bool) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, fmt.Errorf("raglit: enqueue identity: empty path")
	}
	now := time.Now().UnixNano()
	res, err := s.db.Exec(
		`INSERT INTO identity_jobs(path, state, force, tags_only, enqueued_at) VALUES(?, 'pending', ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   state       = 'pending',
		   force       = excluded.force,
		   tags_only   = excluded.tags_only,
		   error       = '',
		   enqueued_at = excluded.enqueued_at,
		   started_at  = 0,
		   finished_at = 0,
		   owner_pid   = 0
		 WHERE identity_jobs.state IN ('done','skipped','error')`,
		path, boolInt(force), boolInt(tagsOnly), now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// enqueueIdentityTx is EnqueueIdentity inside a caller's transaction — used by
// commitDoc so a new transcript and the caption it owes are one atomic fact.
//
// force is set, because this is only reached when the text a caption was written
// from is not the text the document now has. Without it the worker would find an
// existing caption and decline, which is exactly the stale caption being
// replaced. A PERSON's caption is still refused downstream (IdentifyDocument),
// and commitDoc does not queue those at all.
func enqueueIdentityTx(ctx context.Context, tx dbExecer, path string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO identity_jobs(path, state, force, enqueued_at) VALUES(?, 'pending', 1, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   state='pending', force=1, error='', enqueued_at=excluded.enqueued_at,
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

// EnqueueTagsFor queues every given path for the tags-only ask.
func (s *Store) EnqueueTagsFor(paths []string, force bool) (int, error) {
	n := 0
	for _, p := range paths {
		queued, err := s.EnqueueTags(p, force)
		if err != nil {
			return n, err
		}
		if queued {
			n++
		}
	}
	return n, nil
}

// EnqueueMissingTags queues every CAPTIONED document that has no tags — or,
// with force, every captioned document. The backfill for a corpus captioned
// before tags existed.
func (s *Store) EnqueueMissingTags(force bool) (int, error) {
	var paths []string
	if force {
		ids, err := s.Identities()
		if err != nil {
			return 0, err
		}
		for _, st := range ids {
			if !st.Empty() {
				paths = append(paths, st.Path)
			}
		}
	} else {
		var err error
		if paths, err = s.DocumentsMissingTags(); err != nil {
			return 0, err
		}
	}
	return s.EnqueueTagsFor(paths, force)
}

// captionableMissing is every document that OWES a caption: one that has none,
// and one whose caption was written from a transcript the document no longer
// has (gen_text_hash empty or stale). A person's caption is never owed again.
//
// Documents a previous sweep established have nothing to caption are excluded —
// re-queueing those is a guaranteed no-op that would nonetheless read as
// outstanding work forever. --force still reaches them, because "nothing to
// read" stops being true the moment a document is re-OCR'd.
//
// The staleness test is done in Go rather than SQL because the hash is over the
// REASSEMBLED text — overlapping windows are de-overlapped, which sqlite cannot
// express — and it is the same comparison commitDoc makes.
func (s *Store) captionableMissing() ([]string, error) {
	stale, err := s.staleCaptions()
	if err != nil {
		return nil, err
	}
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return append(out, stale...), nil
}

// staleCaptions lists documents whose caption describes a transcript the
// document no longer has — the backlog form of the edge commitDoc fires. A
// caption with no recorded source text (written before the hash existed) counts
// as stale: it is exactly as trustworthy as one that is.
func (s *Store) staleCaptions() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path, gen_text_hash FROM documents
		  WHERE TRIM(gen_name) <> '' AND gen_source <> 'person' ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	type doc struct{ path, hash string }
	var docs []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.path, &d.hash); err != nil {
			rows.Close()
			return nil, err
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	ctx := context.Background()
	var out []string
	for _, d := range docs {
		h, err := s.documentTextHash(ctx, d.path)
		if err != nil || h == d.hash {
			continue
		}
		out = append(out, d.path)
	}
	return out, nil
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
	var force, tagsOnly int
	now := time.Now().UnixNano()
	err := s.db.QueryRow(
		`UPDATE identity_jobs SET state='running', started_at=?, owner_pid=?
		  WHERE id = (SELECT id FROM identity_jobs WHERE state='pending' ORDER BY id LIMIT 1)
		 RETURNING id, path, force, tags_only, enqueued_at`,
		// The claiming process is stamped for the same reason ingest stamps it: a
		// 'running' row outlives the process that owned it, and without the pid a
		// fresh worker cannot tell its own live work from a dead worker's leftovers.
		now, os.Getpid()).
		Scan(&j.ID, &j.Path, &force, &tagsOnly, &j.EnqueuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.State, j.Force, j.TagsOnly, j.StartedAt = "running", force != 0, tagsOnly != 0, now
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

// DefaultIdentitySlots is how wide this worker's caption pipeline is.
//
// It used to be a CAPACITY CLAIM — "two, because that is what the endpoint
// serves concurrently" — and that was a number about a server written down in
// raglit, which is exactly the kind that goes stale when the model layout
// changes. It is no longer one: the identity model has its own admission channel
// (modelchan.go), learned from the server's own backpressure, and that is what
// bounds requests now.
//
// So this is just how many captions may be ASSEMBLED and in flight at once. Four
// rather than two, because the extra callers cost a goroutine each and simply
// wait in Acquire when the model is narrow — while a model that turns out to
// serve several would otherwise be held to two by a constant nobody revisited.
const DefaultIdentitySlots = 4

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
	// textHash fingerprints the INDEXED text as it stood when this job was
	// loaded, so the caption records what it was written from and a later commit
	// can tell whether the transcript has moved. Computed by the loader, off a
	// slot, from the same read.
	textHash string
	// tagCtx is the index's established tag vocabulary as it stood when this job
	// was loaded (Store.TagContext). Read by the loader, off a slot, and carried
	// per task rather than held on the Identifier — one *Identifier is shared by
	// every slot and every index, so a field there would be a data race and
	// would leak one index's vocabulary into the next. Read per job rather than
	// once per drain so a corpus captioned from empty aligns with the terms it
	// has established so far.
	tagCtx string
	// cur is the identity as it stood when the job was loaded. A tags-only job
	// writes tags INTO it, so the caption and its authorship survive untouched.
	cur DocIdentity
	id  DocIdentity
	err error
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
			t.cur = cur
			switch {
			case err != nil:
				t.err = err
			// A tags-only job has the OPPOSITE keep rule: an existing caption is
			// the precondition, not the reason to decline, and a person's is kept
			// BY not touching it rather than by refusing the job. What it declines
			// is a document that already has tags.
			case job.TagsOnly && cur.Empty():
				t.err = fmt.Errorf("raglit: no caption to tag — run identify first")
			case job.TagsOnly && len(cur.ContentTags) > 0 && !job.Force:
				t.err = ErrIdentityKept
			case !job.TagsOnly && (cur.ByPerson() || (!cur.Empty() && !job.Force)):
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
					t.tagCtx = w.Store.TagContext()
					if h, herr := w.Store.documentTextHash(ctx, job.Path); herr == nil {
						t.textHash = h
					}
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
					if t.job.TagsOnly {
						t.id = t.cur
						t.id.ContentTags, t.id.RoleTags, t.err =
							w.Store.identifier.IdentifyTags(ctx, t.text, t.tagCtx)
					} else {
						t.id, t.err = w.Store.identifier.Identify(ctx, t.text, t.tagCtx)
					}
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
			// A tags-only job keeps the hash the CAPTION was written from. The
			// caption is what goes stale when the transcript moves, and stamping
			// it with text no caption was read from would claim it is current and
			// silence the re-arm in commitDoc.
			if !t.job.TagsOnly {
				t.id.TextHash = t.textHash
			}
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
