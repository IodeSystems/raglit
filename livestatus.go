package raglit

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Live status: what an index is doing NOW, for a poller (`raglit status
// --json`, dun's ps).
//
// The rule this file exists for: the status path takes no lock that work
// holds. A daemon that is busy is exactly when someone asks what it is doing,
// and a status that waits for the ingest to finish answers nothing. So:
//
//   - the in-process counters (embed calls, time, tokens, in flight, last
//     activity) are atomics, and the per-minute window is a mutex held only to
//     bump or sum sixty integers — never across a model call;
//   - the queue and corpus counts are SQLite reads. The index is in WAL mode,
//     where a reader neither waits for nor blocks the writer, so an open write
//     transaction (BEGIN IMMEDIATE, held through an ingest's commit) does not
//     delay them;
//   - nothing here touches vecMu, the missing-file cache (MissingCount stats
//     every document under its mutex), or the model channels' wait.
//
// The embed counters are since the process started; they are not stored.

// Index live states, in the order State picks them.
const (
	LiveEmbedding = "embedding"
	LiveIngesting = "ingesting"
	LiveBackedOff = "backed-off"
	LiveError     = "error"
	LiveIdle      = "idle"
)

// IndexLive is one index's live status. Field names are a contract with
// pollers: add fields, never rename one.
type IndexLive struct {
	Project string `json:"project"` // the namespace; "" for an index with none
	Index   string `json:"index"`   // the local name inside the project
	Name    string `json:"name"`    // the full index name the daemon uses
	State   string `json:"state"`   // embedding | ingesting | backed-off | error | idle
	// QueueLength is the ingest jobs waiting (pending); Running is the jobs
	// claimed and in progress; Failed is the jobs that ended in error.
	QueueLength int `json:"queue_length"`
	Running     int `json:"running"`
	Failed      int `json:"failed"`
	Files       int `json:"files"`  // documents indexed
	Chunks      int `json:"chunks"` // fragments indexed
	// FilesLastMinute is the ingest jobs that finished done in the last 60s.
	FilesLastMinute int `json:"files_last_minute"`
	// EmbedCallsLastMinute is the embed requests sent for this index in the
	// last 60s: ingest, re-embed, figure descriptions and query embeds alike.
	EmbedCallsLastMinute int `json:"embed_calls_last_minute"`
	// EmbedCalls, EmbedSeconds and EmbedTokensEstimated are totals since the
	// daemon started. The tokens are ESTIMATED (characters/4, the same
	// convention as the usage report in newworker.go): the embed endpoint
	// returns vectors, not a token count.
	EmbedCalls           int64   `json:"embed_calls"`
	EmbedSeconds         float64 `json:"embed_seconds"`
	EmbedTokensEstimated int64   `json:"embed_tokens_estimated"`
	EmbedInFlight        int     `json:"embed_in_flight"`
	// TransientBackoff is the sources whose last failure was transient (an
	// endpoint down or refusing for load) and that are still waiting out
	// their retry backoff (failedsources.go).
	TransientBackoff int    `json:"transient_backoff"`
	LastError        string `json:"last_error,omitempty"`
	LastErrorAt      string `json:"last_error_at,omitempty"` // RFC3339
	LastActivity     string `json:"last_activity,omitempty"` // RFC3339
}

// activity is a Store's in-process counters. Zero value ready.
type activity struct {
	embedInFlight atomic.Int64
	embedCalls    atomic.Int64
	embedNanos    atomic.Int64
	embedTokens   atomic.Int64
	last          atomic.Int64 // unix nanos of the last embed call's start or end

	mu  sync.Mutex // guards win only; never held across a call
	win minuteWindow
}

// minuteWindow counts events in one-second buckets over the last minute.
type minuteWindow struct {
	sec [60]int64 // the unix second each bucket holds
	n   [60]int64
}

func (w *minuteWindow) add(now time.Time) {
	s := now.Unix()
	i := s % 60
	if w.sec[i] != s {
		w.sec[i], w.n[i] = s, 0
	}
	w.n[i]++
}

func (w *minuteWindow) sum(now time.Time) int {
	s := now.Unix()
	t := 0
	for i := range w.sec {
		if s-w.sec[i] < 60 {
			t += int(w.n[i])
		}
	}
	return t
}

type activityKey struct{}

// withActivity puts the store's counters on ctx, so the embedder — shared by
// every index — reports each request to the index it was made for.
func (s *Store) withActivity(ctx context.Context) context.Context {
	return context.WithValue(ctx, activityKey{}, &s.act)
}

// embedBegin records the start of one embed request and returns its end. A
// ctx with no store on it records nothing.
func embedBegin(ctx context.Context) func(chars int) {
	a, _ := ctx.Value(activityKey{}).(*activity)
	if a == nil {
		return func(int) {}
	}
	start := time.Now()
	a.embedInFlight.Add(1)
	a.last.Store(start.UnixNano())
	a.mu.Lock()
	a.win.add(start)
	a.mu.Unlock()
	return func(chars int) {
		end := time.Now()
		a.embedInFlight.Add(-1)
		a.embedCalls.Add(1)
		a.embedNanos.Add(int64(end.Sub(start)))
		a.embedTokens.Add(int64((chars + 3) / 4))
		a.last.Store(end.UnixNano())
	}
}

// LiveStatus reports what this index is doing now. backedOff says whether a
// model the daemon calls is holding its width down after a 429 (the daemon
// knows its channels; a Store does not). See the file comment for why this
// takes no lock that work holds.
func (s *Store) LiveStatus(ctx context.Context, backedOff bool) (IndexLive, error) {
	now := time.Now()
	var out IndexLive
	var doneAt, errAt, startedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(state='pending'), 0),
		COALESCE(SUM(state='running'), 0),
		COALESCE(SUM(state='error'), 0),
		COALESCE(SUM(state='done' AND finished_at >= ?), 0),
		COALESCE(MAX(CASE WHEN state='done' THEN finished_at END), 0),
		COALESCE(MAX(CASE WHEN state='error' THEN finished_at END), 0),
		COALESCE(MAX(started_at), 0)
		FROM ingest_jobs`, now.Add(-time.Minute).UnixNano()).
		Scan(&out.QueueLength, &out.Running, &out.Failed, &out.FilesLastMinute, &doneAt, &errAt, &startedAt)
	if err != nil {
		return out, err
	}
	if out.Failed > 0 {
		_ = s.db.QueryRowContext(ctx, `SELECT error FROM ingest_jobs WHERE state='error'
			ORDER BY finished_at DESC LIMIT 1`).Scan(&out.LastError)
		if errAt > 0 {
			out.LastErrorAt = time.Unix(0, errAt).UTC().Format(time.RFC3339)
		}
	}
	nd, err := s.q.CountDocuments(ctx)
	if err != nil {
		return out, err
	}
	nf, err := s.q.CountFragments(ctx)
	if err != nil {
		return out, err
	}
	out.Files, out.Chunks = int(nd), int(nf)
	out.TransientBackoff = s.transientBackoff(ctx, now)

	a := &s.act
	a.mu.Lock()
	out.EmbedCallsLastMinute = a.win.sum(now)
	a.mu.Unlock()
	out.EmbedCalls = a.embedCalls.Load()
	out.EmbedSeconds = time.Duration(a.embedNanos.Load()).Seconds()
	out.EmbedTokensEstimated = a.embedTokens.Load()
	out.EmbedInFlight = int(a.embedInFlight.Load())

	last := max(doneAt, errAt, startedAt, a.last.Load())
	if last > 0 {
		out.LastActivity = time.Unix(0, last).UTC().Format(time.RFC3339)
	}
	out.State = liveState(out, backedOff, errAt > doneAt)
	return out, nil
}

// liveState picks the one word for an index: in flight embedding first, then
// ingest work, then a wait the daemon is serving, then a failure nothing has
// superseded.
func liveState(l IndexLive, backedOff, lastWasError bool) string {
	work := l.QueueLength > 0 || l.Running > 0
	switch {
	case l.EmbedInFlight > 0:
		return LiveEmbedding
	case work && backedOff:
		return LiveBackedOff
	case work:
		return LiveIngesting
	case l.TransientBackoff > 0:
		return LiveBackedOff
	case l.Failed > 0 && lastWasError:
		return LiveError
	}
	return LiveIdle
}

// transientBackoff counts the sources whose transient failure is still inside
// its retry backoff. 0 on any error: the table predates nothing here, but a
// status must not fail on a count it only adds.
func (s *Store) transientBackoff(ctx context.Context, now time.Time) int {
	rows, err := s.db.QueryContext(ctx, `SELECT at, attempts FROM failed_sources WHERE kind = ?`, failTransient)
	if err != nil {
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var at int64
		var attempts int
		if rows.Scan(&at, &attempts) != nil {
			return n
		}
		if now.Sub(time.Unix(0, at)) < failBackoff(attempts) {
			n++
		}
	}
	return n
}
