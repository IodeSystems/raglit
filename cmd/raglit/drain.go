package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// jobDrain winds the ingest queue down before the daemon exits or re-execs.
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
type jobDrain struct {
	stopClaims context.CancelFunc // the claim loop ends; running jobs go on
	cutOff     context.CancelFunc // running jobs are cancelled and requeue themselves
	done       <-chan struct{}    // closed when every runner has returned
}

const (
	// stopGrace is how long SIGTERM waits for running jobs. The systemd unit
	// gives the stop TimeoutStopSec=120 and kills after that: the grace, the
	// cut-off and the HTTP shutdown have to fit inside it.
	stopGrace = 90 * time.Second
	// reexecGrace is how long a re-exec (source rebuild, SIGHUP) waits. Nothing
	// outside kills the process here, so it can wait as long as dun waits
	// before an upgrade.
	reexecGrace = 10 * time.Minute
	// cutOffWait is how long cancelled jobs get to write 'pending' back. A job
	// that does not return in time keeps its 'running' row and is marked 'error'
	// at the next start, as before.
	cutOffWait = 10 * time.Second
)

// drain stops claiming and waits up to grace for the running jobs, then
// cancels them. It reports whether every runner returned. Safe to call more
// than once and from more than one goroutine.
func (d *jobDrain) drain(grace time.Duration) bool {
	d.stopClaims()
	select {
	case <-d.done:
		return true
	case <-time.After(grace):
	}
	fmt.Fprintf(os.Stderr, "raglit daemon: jobs still running after %s — stopping them; they go back on the queue\n", grace)
	d.cutOff()
	select {
	case <-d.done:
		return true
	case <-time.After(cutOffWait):
		fmt.Fprintf(os.Stderr, "raglit daemon: a job did not stop within %s — it will show as an error at the next start\n", cutOffWait)
		return false
	}
}
