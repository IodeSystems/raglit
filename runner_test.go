package raglit

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// The property the Runner exists for, through the real runner pool: a
// document stuck on ONE model must not hold up documents that need a different
// one.
//
// The daemon has had three answers to this. First a single serial loop, where a
// forty-minute scan held every index's queue. Then two lanes with slot counts,
// which fixed the symptom by classifying work as heavy or light. Now per-model
// admission, which fixes the cause: what serialised a transcription against an
// embedding was never that they were the same KIND of work, it was one shared
// "the GPU admits one" budget covering models that live on different cards.
//
// The slow document here never finishes — it blocks inside the fetch until the
// test releases it. If the rest can only proceed once it is done, this test
// times out, which is precisely the old behaviour.
func TestRunner_OneStuckDocumentDoesNotStallTheRest(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}

	// Queued FIRST, so any scheduler handing work out in order reaches it before
	// the rest.
	if _, err := st.Enqueue("file:///corpus/twenty-two-page-scan.pdf", ""); err != nil {
		t.Fatal(err)
	}
	const others = 3
	for i := 0; i < others; i++ {
		if _, err := st.Enqueue(fmt.Sprintf("file:///corpus/notes-%d.md", i), ""); err != nil {
			t.Fatal(err)
		}
	}

	release := make(chan struct{})
	var stuckStarted atomic.Int32
	newWorker := func(s *Store) *Worker {
		return &Worker{
			Store: s,
			Fetcher: func(ctx context.Context, url string) (Fetched, error) {
				// Stands in for a long vision call: the scan blocks, everything
				// else returns immediately.
				if LaneFor(url) == LaneHeavy {
					stuckStarted.Add(1)
					select {
					case <-release:
					case <-ctx.Done():
					}
					return Fetched{}, context.Canceled
				}
				return Fetched{Data: []byte("a light document, indexed in milliseconds")}, nil
			},
		}
	}

	r := startRunner(t, reg, 4, newWorker)

	// Establish the premise before testing the conclusion: the slow document must
	// really be in flight, or "the others finished" says nothing.
	for start := time.Now(); stuckStarted.Load() == 0; {
		if time.Since(start) > 10*time.Second {
			t.Fatal("the slow document never started — the test cannot prove anything")
		}
		time.Sleep(10 * time.Millisecond)
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		done, err := st.Jobs("done", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(done) >= others {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d fast documents finished while one was stuck — "+
				"work is queued behind a document it has nothing to do with", len(done), others)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stuckStarted.Load() != 1 {
		t.Fatalf("the slow document ran %d times", stuckStarted.Load())
	}
	close(release)
	r.CutOff()
}

// The queue must never mark more jobs `running` than it has runners.
//
// The claim is what writes `running` to the row, so a claimer that runs ahead of
// its runners reports work that nothing is doing — the same lie owner_pid exists
// to expose, told by the scheduler itself. Observed on the live daemon: a
// one-slot lane ran one job and reported two.
func TestRunner_NeverClaimsMoreThanItCanRun(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := st.Enqueue(fmt.Sprintf("file:///scan-%d.pdf", i), ""); err != nil {
			t.Fatal(err)
		}
	}

	release := make(chan struct{})
	var inFlight atomic.Int32
	newWorker := func(s *Store) *Worker {
		return &Worker{
			Store: s,
			Fetcher: func(ctx context.Context, _ string) (Fetched, error) {
				inFlight.Add(1)
				defer inFlight.Add(-1)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return Fetched{}, context.Canceled
			},
		}
	}

	const runners = 2
	r := startRunner(t, reg, runners, newWorker)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running, err := st.Jobs("running", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(running) > runners {
			t.Fatalf("queue reports %d jobs running with %d runners (actually in flight: %d) "+
				"— the claimer is running ahead of its runners",
				len(running), runners, inFlight.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if inFlight.Load() == 0 {
		t.Fatal("nothing ever ran — the test proved nothing")
	}
	close(release)
	r.CutOff()
}

// startRunner starts a Runner on reg with the given runner count and a worker
// factory that cannot fail. The test's cleanup cuts it off and waits for its
// runners, so none outlives the registry.
func startRunner(t *testing.T, reg *Registry, runners int, newWorker func(*Store) *Worker) *Runner {
	t.Helper()
	r := &Runner{
		Stores:    reg,
		NewWorker: func(s *Store) (*Worker, error) { return newWorker(s), nil },
		Runners:   runners,
		Notef:     t.Logf,
	}
	r.Start(context.Background())
	t.Cleanup(func() {
		r.CutOff()
		<-r.Done()
	})
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for start := time.Now(); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func jobCount(t *testing.T, st *Store, state string) int {
	t.Helper()
	js, err := st.Jobs(state, 20)
	if err != nil {
		t.Fatal(err)
	}
	return len(js)
}

// A Drain lets the running job finish and claims nothing after it: the job ends
// 'done', and the job behind it stays 'pending' for the next process.
func TestRunnerDrain_RunningJobFinishesAndNothingNewIsClaimed(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := st.Enqueue(fmt.Sprintf("file:///notes-%d.md", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	var started atomic.Int32
	d := startRunner(t, reg, 1, func(s *Store) *Worker {
		return &Worker{Store: s, Fetcher: func(ctx context.Context, _ string) (Fetched, error) {
			started.Add(1)
			<-release
			return Fetched{Data: []byte("a short note")}, nil
		}}
	})
	waitFor(t, "the first job to start", func() bool { return started.Load() == 1 })

	drained := make(chan bool)
	go func() { drained <- d.Drain(10 * time.Second) }()
	// Give the claimer a chance to take the second job if it still could.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if !<-drained {
		t.Fatal("drain reported runners still running")
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("%d jobs started; the drain must claim nothing after the stop", n)
	}
	if done, pending := jobCount(t, st, "done"), jobCount(t, st, "pending"); done != 1 || pending != 1 {
		t.Fatalf("done=%d pending=%d, want 1 and 1", done, pending)
	}
}

// A job still running when the grace ends is cancelled and goes back on the
// queue. Before the drain it was cut off by the exit and marked 'error' at the
// next start.
func TestRunnerDrain_JobCutOffAtGraceIsRequeued(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue("file:///scan.pdf", ""); err != nil {
		t.Fatal(err)
	}
	var started atomic.Int32
	d := startRunner(t, reg, 1, func(s *Store) *Worker {
		return &Worker{Store: s, Fetcher: func(ctx context.Context, _ string) (Fetched, error) {
			started.Add(1)
			<-ctx.Done()
			return Fetched{}, ctx.Err()
		}}
	})
	waitFor(t, "the job to start", func() bool { return started.Load() == 1 })

	if !d.Drain(50 * time.Millisecond) {
		t.Fatal("drain reported runners still running after the cut-off")
	}
	if pending, failed := jobCount(t, st, "pending"), jobCount(t, st, "error"); pending != 1 || failed != 0 {
		t.Fatalf("pending=%d error=%d, want the cut-off job back as pending", pending, failed)
	}
}

// A NewWorker that fails leaves the job pending for a later pass, rather than
// running it with no worker or marking it failed.
func TestRunner_FailingNewWorkerLeavesTheJobPending(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue("file:///notes.md", ""); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	r := &Runner{
		Stores: reg,
		NewWorker: func(*Store) (*Worker, error) {
			calls.Add(1)
			return nil, fmt.Errorf("no models configured")
		},
		Runners: 1,
		Notef:   t.Logf,
	}
	r.Start(context.Background())
	t.Cleanup(r.CutOff)
	waitFor(t, "NewWorker to be asked", func() bool { return calls.Load() >= 1 })
	if !r.Drain(10 * time.Second) {
		t.Fatal("drain reported runners still running")
	}
	if pending, failed := jobCount(t, st, "pending"), jobCount(t, st, "error"); pending != 1 || failed != 0 {
		t.Fatalf("pending=%d error=%d, want the job left pending", pending, failed)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("NewWorker called %d times; a failing factory must not spin the queue", n)
	}
}

// The set is read again on every pass: an index created after Start has its
// queue worked.
func TestRunner_WorksAnIndexAddedAfterStart(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	startRunner(t, reg, 2, func(s *Store) *Worker {
		return &Worker{Store: s, Fetcher: func(context.Context, string) (Fetched, error) {
			return Fetched{Data: []byte("a short note")}, nil
		}}
	})
	time.Sleep(100 * time.Millisecond) // the claimer has made a pass with no "late" index
	st, err := reg.Get("late")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue("file:///late.md", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the late index's job to finish", func() bool { return jobCount(t, st, "done") == 1 })
}

// Drain before Start has nothing to wait for.
func TestRunnerDrain_BeforeStartReturns(t *testing.T) {
	if !(&Runner{}).Drain(time.Hour) {
		t.Fatal("Drain before Start reported runners still running")
	}
}

// Done closes only after the caption loop and its in-flight drains have
// returned, so a caller that closes stores after Done never closes one under a
// caption call.
func TestRunnerDone_WaitsForCaptionDrain(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueIdentity("file:///notes.md", false); err != nil {
		t.Fatal(err)
	}
	var started atomic.Int32
	release := make(chan struct{})
	r := &Runner{
		Stores:    reg,
		NewWorker: func(s *Store) (*Worker, error) { return nil, fmt.Errorf("no ingest") },
		Runners:   1,
		Notef:     t.Logf,
		Logf:      t.Logf,
		// A fake caption call: blocks until its context is cancelled, then
		// lingers until released, as a slow cancellation would.
		drainIdentity: func(ctx context.Context, _ *Store, _ int) (int, error) {
			started.Add(1)
			<-ctx.Done()
			<-release
			return 0, ctx.Err()
		},
	}
	r.Start(context.Background())
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		r.CutOff()
		<-r.Done()
	})
	waitFor(t, "the caption drain to start", func() bool { return started.Load() >= 1 })

	r.CutOff()
	select {
	case <-r.Done():
		t.Fatal("Done closed while a caption drain was still running")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done did not close after the caption drain returned")
	}
}

// Drain reports success only when the caption loop has returned too.
func TestRunnerDrain_WaitsForCaptionDrain(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueIdentity("file:///notes.md", false); err != nil {
		t.Fatal(err)
	}
	var started atomic.Int32
	r := &Runner{
		Stores:    reg,
		NewWorker: func(s *Store) (*Worker, error) { return nil, fmt.Errorf("no ingest") },
		Runners:   1,
		Notef:     t.Logf,
		Logf:      t.Logf,
		drainIdentity: func(ctx context.Context, _ *Store, _ int) (int, error) {
			started.Add(1)
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}
	r.Start(context.Background())
	waitFor(t, "the caption drain to start", func() bool { return started.Load() >= 1 })
	if !r.Drain(10 * time.Second) {
		t.Fatal("Drain reported runners still running")
	}
	select {
	case <-r.Done():
	default:
		t.Fatal("Drain returned true but Done is open")
	}
}
