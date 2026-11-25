package raglit

import (
	"context"
	"log"
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

// reembedQueue is the order a re-embed walks the registry's indexes: current
// first, the rest waiting in order. It is in the library layer because the
// progress and priority surface (the HTTP API and the CLI) both read it, and
// the backfill loop that fills it is here too — one place owns the order.
type reembedQueue struct {
	reg   *Registry
	order []string
}

// newReembedQueue builds the queue from the registry's current index list, in
// its (sorted) order.
func newReembedQueue(reg *Registry) *reembedQueue {
	return &reembedQueue{reg: reg, order: reg.Names()}
}

// Progress returns the current index, its remaining fragment count (fragments
// with no vector), and the waiting list. A current index the registry cannot
// open reports 0 remaining and is not advanced: a transient open failure must
// not skip its re-embed.
func (q *reembedQueue) Progress() ReembedProgress {
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

// SetPriority moves a waiting index to the front of the queue. The current
// index is unchanged: a re-embed already in flight finishes before the
// operator's reorder takes effect. Unknown names are ignored.
func (q *reembedQueue) SetPriority(name string) {
	if len(q.order) == 0 || q.order[0] == name {
		return
	}
	for i, n := range q.order {
		if n == name {
			// Build the new order without aliasing q.order's backing array: an
			// append into it would overwrite the tail it is also reading from.
			out := make([]string, 0, len(q.order))
			out = append(out, q.order[0])
			out = append(out, q.order[i])
			out = append(out, q.order[1:i]...)
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
func (q *reembedQueue) Backfill(ctx context.Context, batch int) error {
	for len(q.order) > 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := q.order[0]
		st, err := q.reg.Get(name)
		if err != nil {
			log.Printf("raglit: re-embed: %s: %v", name, err)
			q.order = q.order[1:]
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
		q.order = q.order[1:]
	}
	return nil
}
