package raglit

import (
	"context"
	"log"
	"sync"
)

// Re-embed progress and ordering.
//
// A re-embed runs at daemon start, one index at a time. On a large corpus that
// is hours of degraded semantic search with nothing telling the operator which
// index is next, how far along it is, or letting them start the index they
// search most first. The queue is the operator's view of that: which index is
// re-embedding, how many of its fragments are left, and what is waiting — and
// a way to reorder the waiting list.

// ReembedProgress is one snapshot of a re-embed run: the index being
// re-embedded now, how many of its fragments still have no vector, and the
// indexes waiting in order. Current and Remaining are empty/0 when the run is
// done (every index fully re-embedded).
type ReembedProgress struct {
	Current   string   `json:"current"`
	Remaining int      `json:"remaining"`
	Waiting   []string `json:"waiting"`
}

// ReembedQueue is the order a re-embed walks the registry's indexes: current
// first, the rest waiting in order. It is in the library layer because the
// progress and priority surface (the HTTP API and the CLI) both read it, and
// the backfill loop that fills it is here too — one place owns the order.
//
// It is owned by one daemon and read and reordered from HTTP handlers while
// the backfill loop runs, so the mutex covers the whole order.
type ReembedQueue struct {
	mu    sync.Mutex
	reg   *Registry
	order []string
}

// NewReembedQueue builds the queue from the registry's current index list, in
// its (sorted) order.
func NewReembedQueue(reg *Registry) *ReembedQueue {
	return &ReembedQueue{reg: reg, order: reg.Names()}
}

// Progress returns the current index, its remaining fragment count (fragments
// with no vector), and the waiting list. A current index the registry cannot
// open reports 0 remaining and is not advanced: a transient open failure must
// not skip its re-embed. A nil queue (a daemon with no indexes) reports an
// empty run.
func (q *ReembedQueue) Progress() ReembedProgress {
	if q == nil {
		return ReembedProgress{Waiting: []string{}}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	p := ReembedProgress{Waiting: []string{}}
	if len(q.order) == 0 {
		return p
	}
	p.Current = q.order[0]
	if st, err := q.reg.Get(p.Current); err == nil {
		if n, err := st.MissingVectors(); err == nil {
			p.Remaining = n
		}
	}
	p.Waiting = append(p.Waiting, q.order[1:]...)
	return p
}

// SetPriority moves an index to the front of the queue, making it the
// current index. It does not interrupt the in-flight re-embed of the previous
// current index: that one runs to completion, and the backfill loop picks up
// the new front on its next iteration (its advance is skipped because the
// front changed). Unknown names are ignored.
func (q *ReembedQueue) SetPriority(name string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 || q.order[0] == name {
		return
	}
	for i, n := range q.order {
		if n == name {
			out := make([]string, 0, len(q.order))
			out = append(out, q.order[i])
			out = append(out, q.order[:i]...)
			out = append(out, q.order[i+1:]...)
			q.order = out
			return
		}
	}
}

// Backfill drives the re-embed: one index at a time, filling its fragments
// that have no vector in batches. A paused index is HELD in the queue: the
// loop does not advance past it, so it resumes — at the front — when it is
// unpaused, rather than sitting in partial semantic search until the next
// start. It returns when every index is fully re-embedded, the embedder is
// gone, or ctx is done.
func (q *ReembedQueue) Backfill(ctx context.Context, batch int) error {
	if q == nil {
		return nil
	}
	// The lock is held only around the order's state, never across the slow
	// BackfillVectors call, so a progress read or a priority reorder is never
	// made to wait on an embed.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var name string
		q.mu.Lock()
		if len(q.order) == 0 {
			q.mu.Unlock()
			break
		}
		name = q.order[0]
		q.mu.Unlock()

		st, err := q.reg.Get(name)
		if err != nil {
			log.Printf("raglit: re-embed: %s: %v", name, err)
			q.mu.Lock()
			if len(q.order) > 0 && q.order[0] == name {
				q.order = q.order[1:]
			}
			q.mu.Unlock()
			continue
		}
		// A paused index holds its place: nothing is embedded for it, and it
		// is not advanced past, so the queue reports it as current the whole
		// time it is paused.
		if st.Paused() {
			return nil
		}
		for {
			n, err := st.BackfillVectors(ctx, batch)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
		}
		q.mu.Lock()
		if len(q.order) > 0 && q.order[0] == name {
			q.order = q.order[1:]
		}
		q.mu.Unlock()
	}
	return nil
}
