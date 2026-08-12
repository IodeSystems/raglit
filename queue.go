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
	// raglit's own output is not a source. Refused HERE because this is the one
	// point every ingest path passes through — the sync planner's ignore globs
	// guard only `sync`, and a directory walked by `ingest`/`index`/POST /ingest
	// applied no rules at all. See IsGeneratedSidecar.
	if IsGeneratedSidecar(url) {
		return 0, fmt.Errorf("raglit: enqueue: %s is raglit's own output, not a source document", url)
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
	// The lane is set HERE, in the same transaction, because a row with no lane
	// is claimed by nobody: each lane's claim asks for its own lane by name. A
	// job that reached the queue and was never scheduled is the worst of the
	// failure modes available — it reports `pending` forever and nothing is
	// wrong with it.
	if err := gq(tx).SetJobLane(context.Background(), gen.SetJobLaneParams{
		Lane: string(LaneFor(url)), ID: id,
	}); err != nil {
		return 0, fmt.Errorf("raglit: enqueue: %w", err)
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
func (s *Store) claimNextJob() (*Job, error) { return s.claim("") }

// ClaimNext claims the oldest pending job in this index, whatever it is.
//
// The exported claim the daemon's runners use. There was briefly a lane
// parameter here: the scheduler sorted jobs into `heavy` and `light` and gave
// each a slot count, to stop a slow scan holding up a text file. The problem was
// real and the cure was in the wrong place — what serialised them was not their
// KIND but one shared "the GPU admits one" budget covering three models on three
// cards. Admission is per model now (modelchan.go), so a transcription and an
// embedding take different channels and neither waits on the other. The lane
// column is still written and still reported; nothing schedules on it.
func (s *Store) ClaimNext() (*Job, error) { return s.claim("") }

// ClaimNextInLane claims the oldest pending job in one scheduling lane, or
// (nil, nil) when that lane has nothing here.
//
// The lane belongs in the CLAIM rather than in a filter afterwards. A dispatcher
// that claimed the oldest job and then put it back because it was the wrong kind
// would move the row's state twice per pass and race the other lane for it; a
// dispatcher that claimed and then held it would be the serial queue again under
// a new name.
func (s *Store) ClaimNextInLane(lane Lane) (*Job, error) { return s.claim(lane) }

// BackfillLanes assigns a lane to pending rows that predate the column, and
// reports how many it set.
//
// An index that has been running since before lanes existed has an EMPTY lane on
// every queued row, and no lane claims those — so without this, upgrading the
// daemon silently strands whatever was already in the queue. Pending only:
// a running row is owned, and a terminal one is never claimed again.
func (s *Store) BackfillLanes() (int, error) {
	ctx := context.Background()
	rows, err := s.q.ListUnlanedPendingJobs(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if err := s.q.SetJobLane(ctx, gen.SetJobLaneParams{
			Lane: string(LaneFor(r.Url)), ID: r.ID,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RequeueJob returns a claimed-but-unstarted job to the queue.
//
// Distinct from RetryJob, which is a person deciding to run something again:
// this is the scheduler putting back a row it claimed and then could not hand to
// a runner, because the daemon is shutting down. Without it the row says
// `running` forever with no process behind it — the state reclaimOrphanedJobs
// exists to repair, which is worth not creating on a clean exit.
//
// Restricted to rows that never started, so it cannot rewind a job mid-ingest.
func (s *Store) RequeueJob(id int64) error {
	_, err := s.db.Exec(
		`UPDATE ingest_jobs SET state='pending', started_at=0, owner_pid=0
		  WHERE id = ? AND state='running' AND finished_at = 0`, id)
	return err
}

// SetJobLane records which lane a job actually belongs in. Used by the worker
// once routing has read the bytes and disagreed with the guess from the URL.
func (s *Store) SetJobLane(id int64, lane Lane) error {
	return s.q.SetJobLane(context.Background(), gen.SetJobLaneParams{Lane: string(lane), ID: id})
}

// claim is the shared body: lane "" means any lane, which is what the CLI's
// single-threaded drain wants.
func (s *Store) claim(lane Lane) (*Job, error) {
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	qtx := gq(tx)

	var row gen.GetOldestPendingJobRow
	if lane == "" {
		row, err = qtx.GetOldestPendingJob(ctx)
	} else {
		var r gen.GetOldestPendingJobInLaneRow
		r, err = qtx.GetOldestPendingJobInLane(ctx, string(lane))
		row = gen.GetOldestPendingJobRow(r)
	}
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
	ID        int64  `json:"id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Error     string `json:"error"`
	Fragments int    `json:"fragments"`
	Mode      string `json:"mode"` // 'llm' | 'offline' | '' — segmentation mode
	// Lane is which scheduler ran it: 'heavy' (vision/OCR, one GPU slot) or
	// 'light' (everything else, several at once). See lane.go.
	Lane       string `json:"lane"`
	EnqueuedAt int64  `json:"enqueued_at"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
}

func jobInfoFromRow(j gen.IngestJob) JobInfo {
	return JobInfo{
		ID: j.ID, URL: j.Url, Title: j.Title, State: j.State, Error: j.Error,
		Fragments: int(j.Fragments), Mode: j.Mode, Lane: j.Lane,
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

// Job returns one job by id, whatever its state and however old it is.
//
// Jobs() answers "the last N", which is the wrong shape for a LINK. The health
// report cites a job by id — an ingest that had to fight the endpoint, a failure
// from a batch weeks ago — and a reader following that citation lands on a
// window of the newest rows that does not contain it. Measured on the delano
// index: the cited job was 927, the newest was 1467, and the page rendered two
// hundred rows none of which was the one the link was about.
func (s *Store) Job(id int64) (JobInfo, error) {
	row, err := s.q.GetJob(context.Background(), id)
	if err != nil {
		return JobInfo{}, err
	}
	// Built field by field rather than through jobInfoFromRow: GetJob names its
	// columns, so adding `lane` to the table made its row a distinct type from
	// the full-table one ListJobs scans into.
	return JobInfo{
		ID: row.ID, URL: row.Url, Title: row.Title, State: row.State, Error: row.Error,
		Fragments: int(row.Fragments), Mode: row.Mode, Lane: row.Lane, EnqueuedAt: row.EnqueuedAt,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}, nil
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
	// Lanes is the queue BY SCHEDULING LANE — heavy (vision/OCR, one GPU slot)
	// and light (everything else, several at once). Reported because "12 pending"
	// answers nothing on its own: twelve scans is an afternoon and twelve
	// markdown files is under a minute, and which one it is used to be
	// unknowable without reading every URL.
	Lanes map[string]LaneStatus `json:"lanes"`
}

// LaneStatus is one lane's share of the queue.
type LaneStatus struct {
	Pending int `json:"pending"`
	Running int `json:"running"`
	// Slots is how many jobs this lane runs at once — the number that says
	// whether a pending count is a queue or a moment.
	Slots int `json:"slots"`
}

// NewStatus is a zero Status with a non-nil Items, so an idle queue marshals as
// `"items":[]` rather than `null` — null-vs-empty-array trips naive consumers.
// Every producer of a Status (IndexStatus, the daemon/MCP aggregators) starts here.
func NewStatus() Status {
	return Status{Items: []PendingItem{}, Lanes: map[string]LaneStatus{}}
}

// laneStatus reads the per-lane queue depth, seeding every KNOWN lane so a lane
// with nothing queued reports zero rather than vanishing. A lane that disappears
// from the report when it is idle is one nobody can tell is idle.
func (s *Store) laneStatus() map[string]LaneStatus {
	out := map[string]LaneStatus{}
	for lane, slots := range DefaultLaneSlots {
		out[string(lane)] = LaneStatus{Slots: slots}
	}
	rows, err := s.q.LaneQueueCounts(context.Background())
	if err != nil {
		return out
	}
	for _, r := range rows {
		st := out[r.Lane]
		switch JobState(r.State) {
		case JobPending:
			st.Pending = int(r.N)
		case JobRunning:
			st.Running = int(r.N)
		}
		out[r.Lane] = st
	}
	return out
}

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
	st.Lanes = s.laneStatus()

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
