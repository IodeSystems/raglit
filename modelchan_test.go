package raglit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func widthOf(t *testing.T, c *Channels, model string) int {
	t.Helper()
	for _, s := range c.Stats() {
		if s.Model == model {
			return s.Width
		}
	}
	t.Fatalf("no channel for %q", model)
	return 0
}

// run n calls through a model's channel, reporting them all as clean.
func runClean(t *testing.T, c *Channels, model string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		rel, err := c.Acquire(context.Background(), model)
		if err != nil {
			t.Fatal(err)
		}
		rel(true)
	}
}

// THE POINT OF THE WHOLE FILE: models do not wait on each other.
//
// Before this, vision ran one job at a time because "the GPU admits one" — one
// number for the whole endpoint. chandra, the embedder and Qwen are on three
// different cards, so that number serialised three machines behind one slot.
// Each at width 1 must still give three concurrent calls.
func TestChannels_ModelsDoNotBlockEachOther(t *testing.T) {
	c := NewChannels()
	models := []string{"chandra-ocr-2", "nomic-embed-text", "Qwen3-6-27B-MPT"}

	var wg sync.WaitGroup
	inAll := make(chan struct{})
	held := make(chan struct{}, len(models))
	for _, m := range models {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			rel, err := c.Acquire(context.Background(), m)
			if err != nil {
				t.Error(err)
				return
			}
			held <- struct{}{}
			<-inAll // hold the slot until every model has one
			rel(true)
		}(m)
	}
	for i := 0; i < len(models); i++ {
		select {
		case <-held:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d models got a slot at once — they are sharing one", i, len(models))
		}
	}
	close(inAll)
	wg.Wait()
}

// A model starts at one, because raglit has not been shown otherwise — which is
// also exactly right for the layout in front of it.
func TestChannels_StartsAtOneAndSerializesOneModel(t *testing.T) {
	c := NewChannels()
	rel, err := c.Acquire(context.Background(), "chandra-ocr-2")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.Acquire(ctx, "chandra-ocr-2"); err == nil {
		t.Fatal("a second concurrent call to the same model was admitted at width 1")
	}
	rel(true)
	// And the slot is released, so the next call goes straight through.
	rel2, err := c.Acquire(context.Background(), "chandra-ocr-2")
	if err != nil {
		t.Fatal(err)
	}
	rel2(true)
}

// Clean calls widen a channel; backpressure halves it. Additive increase,
// multiplicative decrease — the pairing that converges when several clients
// share one resource, and raglit IS one of several.
func TestChannels_GrowsOnSuccessAndHalvesOn429(t *testing.T) {
	c := NewChannels()
	const m = "nomic-embed-text"

	if w := func() int { runClean(t, c, m, 1); return widthOf(t, c, m) }(); w != 1 {
		t.Fatalf("width %d after one call, want 1", w)
	}
	// growAfterCleanCalls*width at width 1, then again at width 2.
	runClean(t, c, m, growAfterCleanCalls)
	if w := widthOf(t, c, m); w != 2 {
		t.Fatalf("width %d after %d clean calls, want 2", w, growAfterCleanCalls)
	}
	runClean(t, c, m, growAfterCleanCalls*2)
	if w := widthOf(t, c, m); w != 3 {
		t.Fatalf("width %d, want 3 — growth should slow as the channel widens", w)
	}

	// One 429 halves it, whatever the advertised capacity is. The server's
	// capacity is shared with other traffic, so backpressure means take less.
	c.Note429(m)
	if w := widthOf(t, c, m); w != 1 {
		t.Fatalf("width %d after a 429, want 3/2 = 1", w)
	}
	// And it will not grow again while chilled, however clean the calls are.
	runClean(t, c, m, growAfterCleanCalls*4)
	if w := widthOf(t, c, m); w != 1 {
		t.Fatalf("width %d — a chilled model grew anyway", w)
	}
}

// A model that genuinely serves one slot must SETTLE at one, not probe forever.
//
// With a fixed cooldown the controller is a loop: grow to 2, take a 429, fall
// back to 1, wait the same 30s, grow to 2 again — for as long as the daemon
// runs. Each cycle costs a rejected call and a retry wait on a model that was
// never going to widen.
func TestChannels_RepeatedBackpressureBacksOffTheProbe(t *testing.T) {
	now := time.Now()
	c := NewChannels()
	c.now = func() time.Time { return now }
	const m = "chandra-ocr-2"

	var chills []time.Duration
	for i := 0; i < 4; i++ {
		runClean(t, c, m, 1) // create the channel
		c.Note429(m)
		var got time.Duration
		for _, s := range c.Stats() {
			if s.Model == m && s.Chilled {
				got = chillFor(i + 1)
			}
		}
		if got == 0 {
			t.Fatalf("model not chilled after 429 #%d", i+1)
		}
		chills = append(chills, got)
	}
	for i := 1; i < len(chills); i++ {
		if chills[i] <= chills[i-1] {
			t.Fatalf("cooldown did not lengthen: %v then %v — a single-slot model will probe forever",
				chills[i-1], chills[i])
		}
	}
	if chillFor(99) != maxChill {
		t.Fatalf("cooldown is unbounded at %v — a model narrowed once would never re-probe", chillFor(99))
	}
}

// A model narrowed by a busy spell must recover once the spell passes, or one
// bad minute costs the rest of the session.
func TestChannels_RecoversAfterTheChillPasses(t *testing.T) {
	now := time.Now()
	c := NewChannels()
	c.now = func() time.Time { return now }
	const m = "Qwen3-6-27B-MPT"

	runClean(t, c, m, growAfterCleanCalls) // width 2
	c.Note429(m)                           // → 1, chilled
	if w := widthOf(t, c, m); w != 1 {
		t.Fatalf("width %d after 429, want 1", w)
	}
	now = now.Add(chillFor(1) + time.Second)
	runClean(t, c, m, growAfterCleanCalls)
	if w := widthOf(t, c, m); w != 2 {
		t.Fatalf("width %d after the chill elapsed, want 2 — it never recovered", w)
	}
}

// A failure that is not backpressure must not widen the channel and must not
// narrow it either: a document too large to embed fails at any width.
func TestChannels_APlainFailureIsNotEvidenceEitherWay(t *testing.T) {
	c := NewChannels()
	const m = "nomic-embed-text"
	runClean(t, c, m, growAfterCleanCalls) // width 2
	for i := 0; i < growAfterCleanCalls*4; i++ {
		rel, err := c.Acquire(context.Background(), m)
		if err != nil {
			t.Fatal(err)
		}
		rel(false)
	}
	if w := widthOf(t, c, m); w != 2 {
		t.Fatalf("width %d after a run of plain failures, want it unchanged at 2", w)
	}
}

// An unknown model name is not gated at all, rather than silently serialised
// behind an empty-string channel shared by everything that forgot to say.
func TestChannels_EmptyModelIsNotGated(t *testing.T) {
	c := NewChannels()
	relA, err := c.Acquire(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	relB, err := c.Acquire(ctx, "")
	if err != nil {
		t.Fatal("an unnamed model was gated — everything without a model name would share one slot")
	}
	relA(true)
	relB(true)
}
