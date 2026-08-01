package raglit

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// The shape that made this necessary: a stage spends an hour absorbing
// backpressure, and the only thing recorded is the LAST call's five-minute
// budget. The tally has to hold the whole job.
func TestRetryTallyKeepsTheWholeJobNotTheLastCall(t *testing.T) {
	var tally RetryTally
	for i := 0; i < 30; i++ {
		tally.Observe(llm.RetryEvent{
			Kind: llm.Retry429, Status: 429, Delay: 40 * time.Second,
			Elapsed: 45 * time.Second,
			BP:      &llm.Backpressure{Capacity: 1, InFlight: 1, Waiting: 2},
		})
	}
	tally.Observe(llm.RetryEvent{Kind: llm.RetryGiveUp, Status: 429, Elapsed: 5 * time.Minute})

	s := tally.Take()
	if s.N429 != 30 {
		t.Errorf("N429 = %d, want 30", s.N429)
	}
	if s.GaveUp != 1 {
		t.Errorf("GaveUp = %d, want 1", s.GaveUp)
	}
	if s.Waited != 20*time.Minute {
		t.Errorf("Waited = %s, want 20m — the number that says an hour was spent holding", s.Waited)
	}
	if s.MaxAhead != 2 {
		t.Errorf("MaxAhead = %d, want 2", s.MaxAhead)
	}
	d := s.Detail()
	for _, want := range []string{"30×429", "2 ahead", "20m0s waiting", "1 gave up"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q is missing %q", d, want)
		}
	}
}

// The server's own message is the actionable half of a 5xx, and the one that was
// repeated 228 times into a log while every job said only "status 500".
func TestRetryTallyKeepsTheServersMessage(t *testing.T) {
	var tally RetryTally
	tally.Observe(llm.RetryEvent{
		Kind: llm.Retry5xx, Status: 500,
		Body: "input (14969 tokens) is too large to process. increase the physical batch size (current batch size: 8192)",
	})
	s := tally.Take()
	if !strings.Contains(s.Detail(), "physical batch size") {
		t.Fatalf("the server said what to do and the summary dropped it: %q", s.Detail())
	}
}

// Take must clear, or every job inherits the previous job's history and the
// stage row blames the wrong document.
func TestRetryTallyTakeClears(t *testing.T) {
	var tally RetryTally
	tally.Observe(llm.RetryEvent{Kind: llm.Retry5xx, Status: 503})
	if s := tally.Take(); s.Empty() {
		t.Fatal("first take lost the event")
	}
	if s := tally.Take(); !s.Empty() {
		t.Fatalf("second take carried history into the next job: %+v", s)
	}
}

// A healthy job must record nothing. A stage row on every job is noise, and
// noise is why the real ones went unnoticed.
func TestRetryTallyIsSilentOnAHealthyJob(t *testing.T) {
	var tally RetryTally
	if !tally.Take().Empty() {
		t.Fatal("an untouched tally is not empty")
	}
	// A call that succeeded on its first attempt emits nothing at all.
	tally.Observe(llm.RetryEvent{Kind: llm.RetryRecovered, Attempt: 1})
	if !tally.Take().Empty() {
		t.Fatal("a recovery with no preceding failure counted as trouble")
	}
}

// A nil tally is the no-model configuration and must not panic.
func TestRetryTallyNilIsSafe(t *testing.T) {
	var tally *RetryTally
	tally.Observe(llm.RetryEvent{Kind: llm.Retry429})
	if !tally.Take().Empty() {
		t.Fatal("nil tally returned a non-empty summary")
	}
}

// Observed on a real job: the row read "last: 200 <!DOCTYPE html>", which
// describes nothing that happened. The status came from a RECOVERY and the body
// from an earlier 5xx, because each field was taken from whichever event set it
// last. They are only meaningful together, and only from the event that failed.
func TestRetryTallyDoesNotMixStatusAndBodyAcrossEvents(t *testing.T) {
	var tally RetryTally
	tally.Observe(llm.RetryEvent{Kind: llm.Retry5xx, Status: 503, Body: "<!DOCTYPE html>"})
	tally.Observe(llm.RetryEvent{Kind: llm.RetryRecovered, Status: 200})
	s := tally.Take()
	if s.LastStatus != 503 {
		t.Errorf("LastStatus = %d; a recovery overwrote the failure it recovered from", s.LastStatus)
	}
	if !strings.Contains(s.Detail(), "503") {
		t.Errorf("detail does not name the failure status: %q", s.Detail())
	}
	if strings.Contains(s.Detail(), "200") {
		t.Errorf("detail reports a success as the last failure: %q", s.Detail())
	}
}
