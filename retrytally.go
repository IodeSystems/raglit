package raglit

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// What the LLM client had to survive to serve one job.
//
// llm.Client reports every retry decision through Client.OnRetry — status, the
// server's own message, queue depth, how long it waited, whether it recovered or
// gave up. raglit set no observer, so all of it existed as sampled stderr and
// nothing else: a job row, a stage row and the API could not see any of it.
//
// What that hid, on one daemon log: 244 5xx retries and 95 429 retries across
// the corpus, including 57 calls that each took four consecutive HTTP 500s. Not
// one appears in any index. And the sampling makes even the log an undercount —
// retryLogEvery=10 prints a call's first ten retries and then every tenth, and
// the give-up and recovery paths print nothing at all, so a run of four 500s
// followed by silence could equally be a success or a failure.
//
// The distortion that made it visible: a document's segment stage spent an hour
// making one call per page against a saturated model, and recorded the LAST
// call's error — "retry budget 5m0s exhausted after 5m18s of 429 backpressure".
// Reading it, the hour looks like five minutes of backpressure. Every number
// needed to see otherwise was already being computed and thrown away.

// RetryTally accumulates llm.RetryEvents for one job.
//
// Safe for concurrent use: the worker is single-threaded per index today, but a
// tally that silently under-counts the moment that changes is the same class of
// error it exists to catch.
type RetryTally struct {
	mu sync.Mutex
	s  RetrySummary
}

// RetrySummary is one job's retry history, flattened.
type RetrySummary struct {
	N429, N5xx, NTransport int
	Recovered, GaveUp      int
	// Waited is the sum of the delays the client scheduled — the wall-clock this
	// job spent holding rather than working. The number that says whether an hour
	// was spent waiting or generating, which one call's Elapsed cannot.
	Waited time.Duration
	// Worst is the longest single call's time in the retry loop.
	Worst time.Duration
	// LastStatus / LastBody are the most recent server answer and its own words.
	// The body is the actionable half — "increase the physical batch size" —
	// and is empty far more often than not.
	LastStatus int
	LastBody   string
	// MaxAhead is the deepest queue a fair-share proxy reported us standing in.
	MaxAhead int
}

// Empty reports whether nothing worth recording happened, which is the normal
// case and must stay silent — a stage row on every healthy job is noise, and
// noise is why the real ones were never noticed.
func (s RetrySummary) Empty() bool {
	return s.N429 == 0 && s.N5xx == 0 && s.NTransport == 0 && s.GaveUp == 0
}

// Detail renders the summary for a stage row.
func (s RetrySummary) Detail() string {
	var parts []string
	if s.N429 > 0 {
		p := fmt.Sprintf("%d×429", s.N429)
		if s.MaxAhead > 0 {
			p += fmt.Sprintf(" (up to %d ahead in queue)", s.MaxAhead)
		}
		parts = append(parts, p)
	}
	if s.N5xx > 0 {
		parts = append(parts, fmt.Sprintf("%d×5xx", s.N5xx))
	}
	if s.NTransport > 0 {
		parts = append(parts, fmt.Sprintf("%d×unreachable", s.NTransport))
	}
	if s.Recovered > 0 {
		parts = append(parts, fmt.Sprintf("%d recovered", s.Recovered))
	}
	if s.GaveUp > 0 {
		parts = append(parts, fmt.Sprintf("%d gave up", s.GaveUp))
	}
	out := strings.Join(parts, ", ")
	if s.Waited > 0 {
		out += fmt.Sprintf("; %s waiting", s.Waited.Round(time.Second))
	}
	if s.Worst > 0 {
		out += fmt.Sprintf(", worst call %s", s.Worst.Round(time.Second))
	}
	if s.LastStatus > 0 && s.LastBody != "" {
		out += fmt.Sprintf("; last: %d %s", s.LastStatus, clipRetryBody(s.LastBody))
	}
	return out
}

// clipRetryBody bounds a server message to something a stage row can hold. The
// useful part of these is the front — the numbers and the instruction.
func clipRetryBody(s string) string {
	s = strings.TrimSpace(s)
	const max = 160
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// Observe is the llm.Client.OnRetry hook.
func (t *RetryTally) Observe(e llm.RetryEvent) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch e.Kind {
	case llm.Retry429:
		t.s.N429++
	case llm.Retry5xx:
		t.s.N5xx++
	case llm.RetryTransport:
		t.s.NTransport++
	case llm.RetryRecovered:
		t.s.Recovered++
	case llm.RetryGiveUp:
		t.s.GaveUp++
	}
	t.s.Waited += e.Delay
	if e.Elapsed > t.s.Worst {
		t.s.Worst = e.Elapsed
	}
	if e.Status > 0 {
		t.s.LastStatus = e.Status
	}
	if e.Body != "" {
		t.s.LastBody = e.Body
	}
	if e.BP != nil && e.BP.Waiting > t.s.MaxAhead {
		t.s.MaxAhead = e.BP.Waiting
	}
}

// Take returns the accumulated summary and clears it, so the next job starts
// from zero. One call per job, at the end of it.
func (t *RetryTally) Take() RetrySummary {
	if t == nil {
		return RetrySummary{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.s
	t.s = RetrySummary{}
	return s
}
