package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	gen "github.com/iodesystems/raglit/internal/db"
	"github.com/iodesystems/sqlc-go-codegen-metaquery/metaquery"
	"github.com/iodesystems/sqlc-go-codegen-metaquery/metaquery/mqsqlite"
)

// Lazy ingestion — the ingest queue.
//
// An `ingest` call ENQUEUES a URL and returns immediately; a worker (worker.go)
// drains the queue in the background. Jobs move pending → running → done|error,
// and IndexStatus reports how much is left, at what rate, and an ETA per pending
// item. The queue is a table in the same index file (durable across restarts).
//
// Data access is the generated sqlc layer (internal/db); the jobs LIST uses a
// metaquery Builder for dynamic state-filter + limit. Job/queue tables carry no
// FTS, so nothing here needs raw SQL.

// JobState is the lifecycle of an ingest job.
type JobState string

const (
	JobPending JobState = "pending"
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	JobError   JobState = "error"
)

// Job is one queued ingestion of a URL.
type Job struct {
	ID         int64
	URL        string
	Title      string
	State      JobState
	Error      string
	Fragments  int
	EnqueuedAt int64
	StartedAt  int64
	FinishedAt int64
	// Fresh re-reads the document even when nothing changed: it skips both the
	// unchanged-bytes fast path and the cross-index pool. The escape hatch for a
	// cached result that is wrong for a reason the cache key cannot see.
	Fresh bool
}

// Enqueue adds a pending ingest job for url and returns its id. It does not
// fetch or index anything — a worker does that later (lazy).
//
// A url already waiting is NOT queued again; the existing job's id is returned.
// Without this the queue inflates every time anything requeues — a watch fires,
// a batch is re-run, a sweep is retried — and the work is done two and three
// times over. Measured on the ardley corpus before the check: 162 queued rows
// for 115 distinct documents, one court memorandum queued FIVE times, roughly
// 29% of a nine-hour backlog that was pure repetition.
//
// Deduped against pending and running only, never against done. Re-indexing a
// document that has changed is legitimate and must stay possible; what is never
// useful is the same file sitting in the queue twice at once.
func (s *Store) Enqueue(url, title string) (int64, error) {
	return s.EnqueueFresh(url, title, false)
}

// EnqueueFresh is Enqueue with an explicit re-read flag. A fresh job never
// dedupes against a pending one: the caller is asking for work the queued job
// would specifically NOT do.
func (s *Store) EnqueueFresh(url, title string, fresh bool) (int64, error) {
	if url == "" {
		return 0, fmt.Errorf("raglit: enqueue: empty url")
	}
	// One transaction, so two callers racing on the same url cannot both see an
	// empty queue and both insert.
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("raglit: enqueue: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var existing int64
	err = tx.QueryRow(
		`SELECT id FROM ingest_jobs WHERE url = ? AND state IN ('pending','running') AND fresh >= ? ORDER BY id LIMIT 1`,
		url, boolInt(fresh),
	).Scan(&existing)
	switch {
	case err == nil:
		return existing, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("raglit: enqueue: %w", err)
	}

	id, err := gq(tx).EnqueueJob(context.Background(), gen.EnqueueJobParams{
		Url: url, Title: title, EnqueuedAt: time.Now().UnixNano(),
	})
	if err != nil {
		return 0, fmt.Errorf("raglit: enqueue: %w", err)
	}
	if fresh {
		if _, err := tx.Exec(`UPDATE ingest_jobs SET fresh = 1 WHERE id = ?`, id); err != nil {
			return 0, fmt.Errorf("raglit: enqueue: %w", err)
		}
	}
	return id, tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DedupeQueue collapses duplicate pending jobs for the same url, keeping the
// oldest, and returns how many rows it removed.
//
// For queues that grew before Enqueue deduped. A running job is left alone: a
// worker owns it, and deleting a row out from under one is how a job ends up
// stuck in `running` forever with nothing to finish it.
func (s *Store) DedupeQueue() (int, error) {
	res, err := s.db.Exec(`
		DELETE FROM ingest_jobs
		WHERE state = 'pending'
		  AND id NOT IN (
		    SELECT MIN(id) FROM ingest_jobs WHERE state = 'pending' GROUP BY url
		  )
		  AND url IN (
		    SELECT url FROM ingest_jobs WHERE state = 'running'
		    UNION
		    SELECT url FROM ingest_jobs WHERE state = 'pending'
		  )`)
	if err != nil {
		return 0, fmt.Errorf("raglit: dedupe: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// claimNextJob atomically moves the oldest pending job to running and returns
// it. Returns (nil, nil) when the queue is empty.
func (s *Store) claimNextJob() (*Job, error) {
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	qtx := gq(tx)

	row, err := qtx.GetOldestPendingJob(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixNano()
	// Stamp the claiming process. If we die mid-ingest this row would otherwise
	// read 'running' forever; reclaimOrphanedJobs uses the pid to tell a live
	// worker from a dead one's leftovers.
	if err := qtx.SetJobRunning(ctx, gen.SetJobRunningParams{
		StartedAt: now, OwnerPid: int64(os.Getpid()), ID: row.ID,
	}); err != nil {
		return nil, err
	}
	// Read the fresh flag inside the same transaction that claims the row. Raw
	// SQL because the generated query layer predates the column (see migrate);
	// a job that cannot report it would silently take the cached path, which is
	// the one thing --fresh exists to prevent.
	var fresh int
	if err := tx.QueryRow(`SELECT fresh FROM ingest_jobs WHERE id = ?`, row.ID).Scan(&fresh); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Job{ID: row.ID, URL: row.Url, Title: row.Title, State: JobRunning,
		EnqueuedAt: row.EnqueuedAt, StartedAt: now, Fresh: fresh != 0}, nil
}

// completeJob marks a job done with the fragment count it produced and the
// fragmenter/outcome mode ("text-overlap" | "llm-seg" | "pooled" | "unchanged").
func (s *Store) completeJob(id int64, fragments int, mode string) error {
	return s.q.CompleteJob(context.Background(), gen.CompleteJobParams{
		Fragments: int64(fragments), Mode: mode, FinishedAt: time.Now().UnixNano(), ID: id,
	})
}

// failJob marks a job errored with a message (truncated).
func (s *Store) failJob(id int64, msg string) error {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return s.q.FailJob(context.Background(), gen.FailJobParams{Error: msg, FinishedAt: time.Now().UnixNano(), ID: id})
}

// JobInfo is a full ingest-job row for the review UI's job table (all states).
type JobInfo struct {
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Error      string `json:"error"`
	Fragments  int    `json:"fragments"`
	Mode       string `json:"mode"` // 'llm' | 'offline' | '' — segmentation mode
	EnqueuedAt int64  `json:"enqueued_at"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
}

func jobInfoFromRow(j gen.IngestJob) JobInfo {
	return JobInfo{
		ID: j.ID, URL: j.Url, Title: j.Title, State: j.State, Error: j.Error,
		Fragments: int(j.Fragments), Mode: j.Mode,
		EnqueuedAt: j.EnqueuedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
}

// Jobs lists ingest jobs, newest first, via a metaquery Builder: state filters
// to one lifecycle state (pending|running|done|error); "" or "all" returns every
// state. limit ≤ 0 → 100.
func (s *Store) Jobs(state string, limit int) ([]JobInfo, error) {
	if limit <= 0 {
		limit = 100
	}
	b := gen.WrapListJobs().OrderBy("id", metaquery.Desc)
	if state != "" && state != "all" {
		b = b.Where("state", metaquery.OpEq, state)
	}
	b = b.ApplyPagination(metaquery.PageRequest{Size: limit})
	res, err := mqsqlite.Scan[gen.IngestJob](context.Background(), s.db, b)
	if err != nil {
		return nil, err
	}
	out := make([]JobInfo, len(res.Data))
	for i, j := range res.Data {
		out[i] = jobInfoFromRow(j)
	}
	return out, nil
}

// RetryJob requeues an errored or done job: state → pending, error cleared,
// timestamps reset, so the worker picks it up again. Errors if the job isn't in
// a retryable state.
func (s *Store) RetryJob(id int64) error {
	n, err := s.q.RetryJob(context.Background(), id)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("raglit: job %d not retryable (must be error or done)", id)
	}
	return nil
}

// CancelJob removes a pending job from the queue. Only pending jobs can be
// canceled — a running job is mid-flight and done/error jobs are terminal.
func (s *Store) CancelJob(id int64) error {
	n, err := s.q.CancelJob(context.Background(), id)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("raglit: job %d not cancelable (only pending jobs can be canceled)", id)
	}
	return nil
}

// PendingItem is one queued (not-yet-done) job plus an ETA estimate.
type PendingItem struct {
	ID         int64   `json:"id"`
	URL        string  `json:"url"`
	State      string  `json:"state"`       // pending | running
	ETASeconds float64 `json:"eta_seconds"` // estimate to completion; 0 = unknown
}

// Status is a snapshot of the index + ingest queue.
type Status struct {
	Documents  int           `json:"documents"`    // docs indexed
	Fragments  int           `json:"fragments"`    // fragments indexed
	Done       int           `json:"done"`         // completed jobs
	Running    int           `json:"running"`      // in-flight jobs
	Pending    int           `json:"pending"`      // queued jobs
	Failed     int           `json:"failed"`       // errored jobs
	RatePerMin float64       `json:"rate_per_min"` // recent completion rate (jobs/min); 0 = unknown
	Items      []PendingItem `json:"items"`        // running + pending, in processing order, with ETAs
	// Identity is the CAPTIONING queue (identityqueue.go), which is separate work
	// on the same endpoint: an ingest job is minutes of OCR over many pages, a
	// caption is one bounded call. Reported here because "what is this index
	// still owed" is one question, and a 400-document sweep outstanding is the
	// kind of thing that should not need a second command to notice.
	Identity IdentityQueueStatus `json:"identity"`
}

// NewStatus is a zero Status with a non-nil Items, so an idle queue marshals as
// `"items":[]` rather than `null` — null-vs-empty-array trips naive consumers.
// Every producer of a Status (IndexStatus, the daemon/MCP aggregators) starts here.
func NewStatus() Status { return Status{Items: []PendingItem{}} }

// IndexStatus reports queue progress: counts, a recent processing rate, and a
// per-item ETA (queue position × recent average job duration). ETA/rate are 0
// until at least one job has completed (no basis to estimate).
func (s *Store) IndexStatus() (Status, error) {
	ctx := context.Background()
	st := NewStatus()
	nd, err := s.q.CountDocuments(ctx)
	if err != nil {
		return st, err
	}
	st.Documents = int(nd)
	nf, err := s.q.CountFragments(ctx)
	if err != nil {
		return st, err
	}
	st.Fragments = int(nf)
	if iq, err := s.IdentityQueue(); err == nil {
		st.Identity = iq
	}

	counts, err := s.q.JobStateCounts(ctx)
	if err != nil {
		return st, err
	}
	for _, c := range counts {
		switch JobState(c.State) {
		case JobDone:
			st.Done = int(c.N)
		case JobRunning:
			st.Running = int(c.N)
		case JobPending:
			st.Pending = int(c.N)
		case JobError:
			st.Failed = int(c.N)
		}
	}

	avgSec := s.recentAvgSeconds()
	if avgSec > 0 {
		st.RatePerMin = 60.0 / avgSec
	}

	active, err := s.q.ListActiveJobs(ctx)
	if err != nil {
		return st, err
	}
	for pos, it := range active {
		item := PendingItem{ID: it.ID, URL: it.Url, State: it.State}
		if avgSec > 0 {
			item.ETASeconds = float64(pos+1) * avgSec
		}
		st.Items = append(st.Items, item)
	}
	return st, nil
}

// recentAvgSeconds is the mean wall-clock duration (seconds) of the last 10
// completed jobs, the basis for rate + ETA. 0 when nothing has completed.
func (s *Store) recentAvgSeconds() float64 {
	rows, err := s.q.RecentDoneDurations(context.Background())
	if err != nil {
		return 0
	}
	var total float64
	var n int
	for _, r := range rows {
		total += float64(r.FinishedAt-r.StartedAt) / 1e9
		n++
	}
	if n == 0 {
		return 0
	}
	return total / float64(n)
}

// ForgetJob deletes a terminal job row and its stages.
//
// Not a cancel and not a retry: this is "that attempt is not coming back and I
// do not want to be told about it again". The health report already drops rows
// whose file is gone, because that is decidable; this is for the ones only a
// person can call — a remote URL that is genuinely dead, a failure nobody is
// going to chase.
//
// Refused on a live job. A pending job has a cancel and a running one is
// mid-flight; deleting either loses work that is still in front of somebody.
//
// It removes evidence, and that is the point of restricting it to a person's
// explicit act. What the row records is an ATTEMPT, not a fact about the corpus
// — nothing cites it, and a log nobody can prune is one nobody reads.
func (s *Store) ForgetJob(id int64) error {
	res, err := s.db.Exec(
		`DELETE FROM ingest_jobs WHERE id = ? AND state IN ('error','done')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("raglit: job %d not forgettable (only errored or done jobs; a pending job is canceled, a running one is in flight)", id)
	}
	// job_stages has ON DELETE CASCADE, but foreign_keys is a per-connection
	// pragma and this index has outlived a build where it was not set on all of
	// them — which is how 140 orphan stage rows got here. Explicit costs nothing.
	_, _ = s.db.Exec(`DELETE FROM job_stages WHERE job_id = ?`, id)
	return nil
}
