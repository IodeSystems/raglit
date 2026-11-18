package raglit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// blockingVec blocks every Embed until release is closed, after saying it
// started.
type blockingVec struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingVec) Embed(ctx context.Context, _ string, input []string) ([][]float32, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(input))
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

// The status path answers while work holds every lock it holds: an embed
// call in flight, the SQLite write lock (an open BEGIN IMMEDIATE), the vector
// lock held exclusive, and the missing-file cache's mutex. A status that took
// any of them would wait here until the test's deadline.
func TestLiveStatus_AnswersWhileWorkHoldsLocks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bv := &blockingVec{started: make(chan struct{}, 1), release: make(chan struct{})}
	s.SetEmbedder(NewEmbedder(bv, "m"))

	// One job claimed (running) and one waiting.
	if _, err := s.Enqueue("file:///a.md", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("file:///b.md", "B"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNext(); err != nil {
		t.Fatal(err)
	}

	ingested := make(chan error, 1)
	go func() {
		ingested <- s.Ingest(context.Background(), Document{Path: "file:///a.md", Title: "A",
			Fragments: []Fragment{{Text: "walrus text"}}})
	}()
	select {
	case <-bv.started:
	case <-time.After(5 * time.Second):
		t.Fatal("embed never started")
	}

	// The SQLite write lock, held by another connection.
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	s.vecMu.Lock()
	s.missing.mu.Lock()

	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	t0 := time.Now()
	l, err := s.LiveStatus(sctx, false)
	took := time.Since(t0)

	s.missing.mu.Unlock()
	s.vecMu.Unlock()
	_, _ = conn.ExecContext(ctx, `ROLLBACK`)
	conn.Close()

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("status blocked behind work's locks (%s)", took)
		}
		t.Fatal(err)
	}
	if took > 2*time.Second {
		t.Fatalf("status took %s", took)
	}
	if l.State != LiveEmbedding || l.EmbedInFlight != 1 {
		t.Errorf("state %q in flight %d, want embedding 1", l.State, l.EmbedInFlight)
	}
	if l.Running != 1 || l.QueueLength != 1 {
		t.Errorf("running %d queue %d, want 1 1", l.Running, l.QueueLength)
	}
	if l.EmbedCallsLastMinute != 1 {
		t.Errorf("embed calls last minute = %d, want 1 (counted at the start)", l.EmbedCallsLastMinute)
	}

	close(bv.release)
	if err := <-ingested; err != nil {
		t.Fatal(err)
	}
	l, err = s.LiveStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if l.EmbedInFlight != 0 || l.EmbedCalls != 1 || l.EmbedTokensEstimated <= 0 || l.EmbedSeconds <= 0 {
		t.Errorf("after the call: %+v", l)
	}
	if l.Files != 1 || l.Chunks != 1 {
		t.Errorf("files %d chunks %d, want 1 1", l.Files, l.Chunks)
	}
	if l.LastActivity == "" {
		t.Error("no last activity")
	}
}

func TestLiveState(t *testing.T) {
	for _, c := range []struct {
		l          IndexLive
		backed     bool
		lastWasErr bool
		want       string
	}{
		{IndexLive{}, false, false, LiveIdle},
		{IndexLive{EmbedInFlight: 1}, true, false, LiveEmbedding},
		{IndexLive{QueueLength: 3}, false, false, LiveIngesting},
		{IndexLive{Running: 1}, true, false, LiveBackedOff},
		{IndexLive{TransientBackoff: 2}, false, false, LiveBackedOff},
		{IndexLive{Failed: 1}, false, true, LiveError},
		{IndexLive{Failed: 1}, false, false, LiveIdle}, // a later success superseded it
	} {
		if got := liveState(c.l, c.backed, c.lastWasErr); got != c.want {
			t.Errorf("liveState(%+v, %v, %v) = %q, want %q", c.l, c.backed, c.lastWasErr, got, c.want)
		}
	}
}

// Failures surface as last_error with its time, and an index whose last job
// failed reads "error".
func TestLiveStatus_LastError(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.Enqueue("file:///bad.md", "Bad")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNext(); err != nil {
		t.Fatal(err)
	}
	if err := s.failJob(id, "extract: unreadable"); err != nil {
		t.Fatal(err)
	}
	l, err := s.LiveStatus(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if l.State != LiveError || l.LastError != "extract: unreadable" || l.LastErrorAt == "" || l.Failed != 1 {
		t.Errorf("%+v", l)
	}
	if _, err := time.Parse(time.RFC3339, l.LastErrorAt); err != nil {
		t.Errorf("last_error_at %q: %v", l.LastErrorAt, err)
	}
}
