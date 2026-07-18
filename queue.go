package raglit

import (
	"database/sql"
	"fmt"
	"time"
)

// Lazy ingestion — the ingest queue.
//
// An `ingest` MCP tool / CLI call ENQUEUES a URL and returns immediately; a
// worker (worker.go) drains the queue in the background. That's why callers can
// ask for status: jobs move pending → running → done|error, and IndexStatus
// reports how much is left, at what rate, and an ETA per pending item.
//
// The queue is just a table in the same index file, so it's durable across
// restarts and shares the one portable .sqlite unit.

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
}

// Enqueue adds a pending ingest job for url and returns its id. It does not
// fetch or index anything — a worker does that later (lazy).
func (s *Store) Enqueue(url, title string) (int64, error) {
	if url == "" {
		return 0, fmt.Errorf("raglit: enqueue: empty url")
	}
	res, err := s.db.Exec(
		`INSERT INTO ingest_jobs(url, title, state, enqueued_at) VALUES(?,?,?,?)`,
		url, title, string(JobPending), time.Now().UnixNano())
	if err != nil {
		return 0, fmt.Errorf("raglit: enqueue: %w", err)
	}
	return res.LastInsertId()
}

// claimNextJob atomically moves the oldest pending job to running and returns
// it. Returns (nil, nil) when the queue is empty.
func (s *Store) claimNextJob() (*Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var j Job
	err = tx.QueryRow(
		`SELECT id, url, title, enqueued_at FROM ingest_jobs
		 WHERE state='pending' ORDER BY id LIMIT 1`).
		Scan(&j.ID, &j.URL, &j.Title, &j.EnqueuedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixNano()
	if _, err := tx.Exec(
		`UPDATE ingest_jobs SET state='running', started_at=? WHERE id=?`, now, j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.State, j.StartedAt = JobRunning, now
	return &j, nil
}

// completeJob marks a job done with the fragment count it produced.
func (s *Store) completeJob(id int64, fragments int) error {
	_, err := s.db.Exec(
		`UPDATE ingest_jobs SET state='done', fragments=?, error='', finished_at=? WHERE id=?`,
		fragments, time.Now().UnixNano(), id)
	return err
}

// failJob marks a job errored with a message (truncated).
func (s *Store) failJob(id int64, msg string) error {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, err := s.db.Exec(
		`UPDATE ingest_jobs SET state='error', error=?, finished_at=? WHERE id=?`,
		msg, time.Now().UnixNano(), id)
	return err
}

// PendingItem is one queued (not-yet-done) job plus an ETA estimate.
type PendingItem struct {
	ID         int64   `json:"id"`
	URL        string  `json:"url"`
	State      string  `json:"state"`        // pending | running
	ETASeconds float64 `json:"eta_seconds"`  // estimate to completion; 0 = unknown
}

// Status is a snapshot of the index + ingest queue.
type Status struct {
	Documents  int           `json:"documents"`   // docs indexed
	Fragments  int           `json:"fragments"`   // fragments indexed
	Done       int           `json:"done"`        // completed jobs
	Running    int           `json:"running"`     // in-flight jobs
	Pending    int           `json:"pending"`     // queued jobs
	Failed     int           `json:"failed"`      // errored jobs
	RatePerMin float64       `json:"rate_per_min"` // recent completion rate (jobs/min); 0 = unknown
	Items      []PendingItem `json:"items"`       // running + pending, in processing order, with ETAs
}

// IndexStatus reports queue progress: counts, a recent processing rate, and a
// per-item ETA (queue position × recent average job duration). ETA/rate are 0
// until at least one job has completed (no basis to estimate).
func (s *Store) IndexStatus() (Status, error) {
	var st Status
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&st.Documents); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments`).Scan(&st.Fragments); err != nil {
		return st, err
	}
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM ingest_jobs GROUP BY state`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			rows.Close()
			return st, err
		}
		switch JobState(state) {
		case JobDone:
			st.Done = n
		case JobRunning:
			st.Running = n
		case JobPending:
			st.Pending = n
		case JobError:
			st.Failed = n
		}
	}
	rows.Close()

	avgSec := s.recentAvgSeconds()
	if avgSec > 0 {
		st.RatePerMin = 60.0 / avgSec
	}

	// Items in processing order: running first, then pending by id. ETA is
	// (position+1) × avg job duration.
	irows, err := s.db.Query(
		`SELECT id, url, state FROM ingest_jobs
		 WHERE state IN ('running','pending')
		 ORDER BY CASE state WHEN 'running' THEN 0 ELSE 1 END, id`)
	if err != nil {
		return st, err
	}
	defer irows.Close()
	pos := 0
	for irows.Next() {
		var it PendingItem
		if err := irows.Scan(&it.ID, &it.URL, &it.State); err != nil {
			return st, err
		}
		if avgSec > 0 {
			it.ETASeconds = float64(pos+1) * avgSec
		}
		st.Items = append(st.Items, it)
		pos++
	}
	return st, irows.Err()
}

// recentAvgSeconds is the mean wall-clock duration (seconds) of the last 10
// completed jobs, the basis for rate + ETA. 0 when nothing has completed.
func (s *Store) recentAvgSeconds() float64 {
	rows, err := s.db.Query(
		`SELECT started_at, finished_at FROM ingest_jobs
		 WHERE state='done' AND started_at>0 AND finished_at>=started_at
		 ORDER BY finished_at DESC LIMIT 10`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var total float64
	var n int
	for rows.Next() {
		var start, fin int64
		if err := rows.Scan(&start, &fin); err != nil {
			return 0
		}
		total += float64(fin-start) / 1e9
		n++
	}
	if n == 0 {
		return 0
	}
	return total / float64(n)
}
