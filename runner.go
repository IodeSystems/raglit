package raglit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// StoreSet is the indexes a Runner works: their names, and the open store for
// a name. It is read again on every pass, so a set that gains an index while
// the Runner runs (a Registry does, when an index is created) has that index's
// queue worked from the next pass on. *Registry satisfies it.
type StoreSet interface {
	Names() []string
	// Existing returns the open store for name, or an error when there is none.
	// It must not create an index.
	Existing(name string) (*Store, error)
}

// Runner is the ingest scheduler: one pool of runners draining every index's
// queue, the identity (captioning) queue beside it, and the drain that winds
// both down before a stop or a re-exec.
//
// Fill the fields, call Start once, and call Drain (or CutOff) to stop.
//
// The identity queue drains only on a store whose identifier is set
// (Store.SetIdentifier); on any other it is skipped without a log line, because
// no identity model is a normal configuration. NewWorker sets it, but Runner
// calls NewWorker only when an ingest runner takes a job from that index. So
// set each store's identifier before Start (raglitd does, through
// Registry.SetIdentifier), or captions queued on an index with no ingest since
// Start wait until that index's next ingest.
type Runner struct {
	// Stores is the set of indexes whose queues are worked. Required.
	Stores StoreSet
	// NewWorker builds the worker for one index. It is called once per index per
	// runner, the first time that runner takes a job from the index. Required.
	// A NewWorker that returns an error leaves the job pending for a later pass.
	NewWorker func(*Store) (*Worker, error)
	// Runners is how many ingest jobs may run at once, across every index.
	// < 1 → DefaultIngestRunners.
	Runners int
	// IdentitySlots is each index's caption pipeline width (IdentityWorker.Slots).
	// 0 → DefaultIdentitySlots.
	IdentitySlots int
	// Logf receives the per-job and per-index log lines. nil → log.Printf.
	Logf func(format string, args ...any)
	// Notef receives the drain's notices (a cut-off, a job that did not stop).
	// nil → a "raglit: " line on stderr.
	Notef func(format string, args ...any)

	stopClaims context.CancelFunc // the claim loop ends; running jobs go on
	cutOff     context.CancelFunc // running jobs are cancelled and requeue themselves
	done       chan struct{}      // closed when every runner has returned
}

// DefaultIngestRunners is how many documents may be in flight at once.
//
// Not a capacity claim — the model channels are that. It is how many documents
// can be PARTWAY through, and it wants to be comfortably more than the number of
// models, so that a document waiting on the vision channel never occupies the
// slot a document needing only embedding could use. Runners are goroutines; the
// scarce thing is downstream.
const DefaultIngestRunners = 8

// CutOffWait is how long cancelled jobs get to write 'pending' back after a
// Drain's grace ends. A job that does not return in time keeps its 'running'
// row and is marked 'error' at the next start, as before.
const CutOffWait = 10 * time.Second

// workerRetry is how long a runner waits after NewWorker failed before it
// takes another job, so a factory that keeps failing does not spin the queue.
const workerRetry = 5 * time.Second

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (r *Runner) notef(format string, args ...any) {
	if r.Notef != nil {
		r.Notef(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, "raglit: "+format+"\n", args...)
}

// Start begins working the queues and returns at once.
//
// It returns two contexts, both under parent. claim ends when the Runner stops
// claiming (Drain, CutOff, or parent ending); work ends when running jobs are
// cancelled (a Drain past its grace, CutOff, or parent ending). They are two
// contexts so a stop can let running jobs finish. A caller may run its own
// background loops under claim and its own request-scoped work under work, so
// they stop with the queue.
//
// Start must be called once.
func (r *Runner) Start(parent context.Context) (claim, work context.Context) {
	work, r.cutOff = context.WithCancel(parent)
	claim, r.stopClaims = context.WithCancel(work)
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		r.runIngest(claim, work)
	}()
	// Captions, on their own queue and their own slot budget. See runIdentity.
	go r.runIdentity(claim)
	return claim, work
}

// Done is closed when every ingest runner has returned. nil before Start.
func (r *Runner) Done() <-chan struct{} { return r.done }

// CutOff cancels the running jobs now; they requeue themselves. It also ends
// the claiming. Safe before Start and more than once.
func (r *Runner) CutOff() {
	if r.cutOff != nil {
		r.cutOff()
	}
}

// Drain winds the ingest queue down before the process exits or re-execs.
//
// Before it, a stop cut off whatever was running. The process exited under the
// jobs, the next start found their rows still 'running' with a dead pid, and
// reclaimOrphanedJobs marked them 'error' — correct for a crash, wrong for a
// restart that someone asked for. Every restart of a busy daemon left errors to
// retry by hand. The re-exec paths waited for an idle queue instead, while the
// queue went on claiming, so a busy queue could hold a re-exec back without end.
//
// A drain stops claiming first, then gives the running jobs a grace period to
// finish, then cancels them. A job cancelled this way goes back to 'pending'
// (Worker.ProcessJob), so the next process runs it. Captions and re-embedding
// are not drained: an orphaned caption is requeued at start
// (ReclaimIdentityJobs), and re-embedding resumes from the fragments that still
// have no vector.
//
// Drain reports whether every runner returned. Safe to call more than once and
// from more than one goroutine. Before Start it returns true at once.
func (r *Runner) Drain(grace time.Duration) bool {
	if r.done == nil {
		return true
	}
	r.stopClaims()
	select {
	case <-r.done:
		return true
	case <-time.After(grace):
	}
	r.notef("jobs still running after %s — stopping them; they go back on the queue", grace)
	r.cutOff()
	select {
	case <-r.done:
		return true
	case <-time.After(CutOffWait):
		r.notef("a job did not stop within %s — it will show as an error at the next start", CutOffWait)
		return false
	}
}

// runIngest drains every index's queue with a pool of runners.
//
// ONE pool, and deliberately generous: the thing that limits concurrency is no
// longer here. It is the per-MODEL admission channel (modelchan.go), and a
// runner blocked in Acquire costs a goroutine and nothing else.
//
// This replaces a two-lane scheduler with per-lane slot counts, which replaced a
// single serial loop. The lanes fixed the right problem the wrong way: a slow
// scan really must not hold up a text file, but the reason it did was never that
// the two were the same KIND of work — it was that vision, segmentation and
// embedding were all being counted against one "the GPU admits one" slot. They
// are three models, usually on three cards. Gate each model and the lane
// classification has nothing left to decide: a transcription and an embedding
// take different channels and simply proceed.
//
// What survives from the lanes is the shape, not the limit: a claimer per pass
// walking indexes in turn, resuming where it stopped so one busy index cannot
// take every turn.
//
// claim ends the claiming; work cancels the jobs already running. They are two
// contexts so a stop can let running jobs finish (Drain).
func (r *Runner) runIngest(claim, work context.Context) {
	// Strand nothing on upgrade: rows queued before lanes existed carry an empty
	// lane, which nothing now reads — but the backfill also proves the column is
	// present, and costs one query per index at startup.
	for _, name := range r.Stores.Names() {
		if st, err := r.Stores.Existing(name); err == nil {
			_, _ = st.BackfillLanes()
		}
	}
	r.runQueue(claim, work)
}

// runQueue is the claim loop and the runner pool. A test supplies a NewWorker
// whose fetch and model calls it controls, and so asserts the property this
// exists for: that a document waiting on one model does not hold up a document
// that needs a different one.
//
// When claim ends, no new job is claimed and runQueue returns once the running
// jobs are done. When work ends, the running jobs are cancelled and requeue
// themselves (Worker.ProcessJob).
func (r *Runner) runQueue(claim, work context.Context) {
	ctx := claim
	runners := r.Runners
	if runners < 1 {
		runners = DefaultIngestRunners
	}
	type claimed struct {
		index string
		job   *Job
	}
	ch := make(chan claimed)
	free := make(chan struct{}, runners)
	for i := 0; i < runners; i++ {
		free <- struct{}{}
	}

	var wg sync.WaitGroup
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workers := map[string]*Worker{}
			for c := range ch {
				if st, err := r.Stores.Existing(c.index); err == nil {
					w := workers[c.index]
					if w == nil {
						if w, err = r.NewWorker(st); err != nil {
							r.logf("raglit: queue %s: job %d: no worker: %v", c.index, c.job.ID, err)
							_ = st.RequeueJob(c.job.ID)
							select {
							case <-ctx.Done():
							case <-time.After(workerRetry):
							}
							free <- struct{}{}
							continue
						}
						workers[c.index] = w
					}
					if err := w.ProcessJob(work, c.job); err != nil {
						r.logf("raglit: queue %s: job %d: %v", c.index, c.job.ID, err)
					}
				}
				free <- struct{}{}
			}
		}()
	}

	// A slot token is taken BEFORE claiming, because the claim is what writes
	// `running` to the row: claiming ahead of a free runner makes the queue
	// report work that nothing is doing.
	cursor := 0
	for ctx.Err() == nil {
		select {
		case <-free:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		names := r.Stores.Names()
		got := false
		for range names {
			if ctx.Err() != nil || len(names) == 0 {
				break
			}
			name := names[cursor%len(names)]
			cursor++
			st, err := r.Stores.Existing(name)
			if err != nil {
				continue
			}
			job, err := st.ClaimNext()
			if err != nil || job == nil {
				continue
			}
			select {
			case ch <- claimed{index: name, job: job}:
				got = true
			case <-ctx.Done():
				// The slot token goes back below, with got false. Returning it
				// here as well put one token too many in free; with every runner
				// idle that send blocked, and the claim loop never returned.
				_ = st.RequeueJob(job.ID)
			}
			break
		}
		if !got {
			free <- struct{}{}
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	close(ch)
	wg.Wait()
}

// runIdentity drains every index's captioning queue.
//
// It used to run ONE INDEX AT A TIME, with a hand-set slot count, because "the
// slot budget belongs to the endpoint, not to an index". That reasoning was
// right about the problem and wrong about where the budget lives: it belongs to
// the MODEL. Two indexes captioning at once are competing for the identity
// model's channel, which now counts them — so they no longer have to be
// serialised here to avoid over-asking, and a caption no longer waits on an
// index it has nothing to do with.
//
// Still separate from the ingest runners because the work is different: an
// ingest job is minutes of OCR over many pages, a caption is one bounded call,
// and putting captions on the ingest queue would make a 400-document sweep block
// every incoming document behind it.
func (r *Runner) runIdentity(ctx context.Context) {
	slots := r.IdentitySlots
	for ctx.Err() == nil {
		var wg sync.WaitGroup
		var did atomic.Bool
		for _, name := range r.Stores.Names() {
			if ctx.Err() != nil {
				break
			}
			st, err := r.Stores.Existing(name)
			if err != nil {
				continue
			}
			if st.Paused() {
				continue
			}
			// A row left 'running' by a dead process is work nobody is doing.
			if n, err := st.ReclaimIdentityJobs(); err == nil && n > 0 {
				r.logf("raglit: identity queue %s: requeued %d orphaned job(s)", name, n)
			}
			q, err := st.IdentityQueue()
			if err != nil || q.Pending == 0 {
				continue
			}
			r.logf("raglit: identity queue %s: %d pending", name, q.Pending)
			// Indexes drain CONCURRENTLY now. They used to go one at a time,
			// because two indexes each draining "two at a time" was four requests
			// at a server that ran two — a real problem, solved in the wrong
			// place. The identity model's admission channel counts every caller
			// in this process, so over-asking is bounded where the resource
			// actually is, and a 400-document sweep on one index no longer holds
			// every other index's captions behind it.
			wg.Add(1)
			go func(name string, st *Store) {
				defer wg.Done()
				n, werr := (&IdentityWorker{Store: st, Slots: slots}).Drain(ctx)
				if werr != nil && !errors.Is(werr, ErrNoIdentifier) {
					r.logf("raglit: identity queue %s: %v", name, werr)
				}
				if n > 0 {
					did.Store(true)
					r.logf("raglit: identity queue %s: %d done", name, n)
				}
			}(name, st)
		}
		wg.Wait()
		if !did.Load() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}
