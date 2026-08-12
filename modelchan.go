package raglit

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// Admission, per MODEL — not per endpoint, and not per kind of work.
//
// Everything before this modelled the SERVER as one resource with one number:
// the ingest queue ran vision one job at a time because "the GPU admits one",
// the caption queue held two slots because "that is what the endpoint serves".
// Both numbers were guesses about a machine, written down in raglit, and both
// were wrong as soon as the layout changed. They are wrong now: the vision model
// and the segmentation model live on DIFFERENT GPUs, so serialising them behind
// one slot stops two cards from working at once for no reason at all.
//
// The unit that actually has a capacity is a MODEL. So each model name gets its
// own channel, and nothing coordinates across them.
//
// The width is LEARNED, because the server is the only thing that knows it and
// it already says so. corrallm answers a saturated backend with a 429 carrying
// Retry-After and its queue state, and agentkit surfaces every one of those
// through Client.OnRetry (llm.RetryEvent, with .BP). So the control law is the
// obvious one for a resource shared with strangers:
//
//   - grow slowly while calls succeed — additive, and only after a full width's
//     worth of clean calls, so one lucky moment does not open the taps;
//   - halve on a 429, immediately, and refuse to grow again for a cooldown.
//
// NOT pinned to the advertised X-RateLimit-Capacity, deliberately, and this is
// the point the user made that the header alone would have got wrong: capacity
// is the SERVER's total, and raglit is not the only client. Other traffic is
// present. Backpressure means "you specifically are asking for too much right
// now", whatever the nameplate says, so the reply to a 429 is to take less —
// never to expand toward a number somebody else is already using.
//
// Waiting HERE rather than inside the server is the other half. A request parked
// in corrallm's queue is invisible: raglit cannot see it, cannot resume it, and
// cannot tell it apart from work that is running. Held at this side it is still
// a pending job with a queue depth somebody can read.

// Channel-width bounds and the shape of the control law.
const (
	// defaultChannelWidth is where a model starts: ONE.
	//
	// It is the only honest starting point, and it happens to be exactly right
	// for the layout in front of it today — chandra, the embedder and Qwen are
	// each loaded on their own card and each serves a single slot. Starting at
	// two would make raglit's very first pair of concurrent calls to any model a
	// self-inflicted 429.
	//
	// It is NOT a claim that a model serves one. It is the claim that raglit has
	// not yet been shown otherwise, which is true of every model the first time
	// it is called. Widening is what evidence is for; the layout is expected to
	// change and nothing here has to be edited when it does.
	//
	// The concurrency that matters is already unlocked by this being PER MODEL:
	// one slot each for three models on three cards is three calls at once,
	// where the old single "heavy" slot allowed one.
	defaultChannelWidth = 1
	// maxChannelWidth caps growth. A ceiling exists because the growth signal is
	// "nothing has pushed back yet", which is also what an endpoint with a very
	// deep queue looks like right up until it collapses.
	maxChannelWidth = 8
	// minChannelWidth is one: a model is never closed entirely, or the work that
	// would prove it recovered can never run.
	minChannelWidth = 1
	// growAfterCleanCalls is how many consecutive successful calls at the current
	// width earn one more slot. Scaled BY the width, so growth gets harder as the
	// channel widens rather than compounding.
	growAfterCleanCalls = 4
	// chillAfter429 is the FIRST cooldown after backpressure — how long a model
	// refuses to grow again.
	//
	// It doubles per consecutive 429 (see chillFor), because a fixed cooldown
	// against a genuinely single-slot model is a probe loop: grow to 2, take a
	// 429, fall back to 1, wait, grow to 2 again, forever. Backing the probe off
	// means a model that really does serve one settles at one and stops asking,
	// while one that was merely busy is retried soon.
	chillAfter429 = 30 * time.Second
	// maxChill bounds that doubling, so a model narrowed during an outage still
	// re-probes within a working session rather than staying narrow until restart.
	maxChill = 16 * chillAfter429
)

// chillFor is the cooldown after the nth consecutive 429, doubling and capped.
func chillFor(consecutive int) time.Duration {
	d := chillAfter429
	for i := 1; i < consecutive && d < maxChill; i++ {
		d *= 2
	}
	if d > maxChill {
		d = maxChill
	}
	return d
}

// Channels holds one admission channel per model name.
type Channels struct {
	mu sync.Mutex
	m  map[string]*modelChannel
	// Max is the widest any channel may grow. 0 → maxChannelWidth.
	//
	// The one knob worth configuring, because the ceiling is the only part of
	// this that depends on WHAT KIND of endpoint is behind it rather than on
	// evidence. A local card serving one model at a time is never going past a
	// handful; a hosted provider bought for throughput will take far more, and
	// there the ceiling — not the provider — would be the limit. Everything else
	// is learned.
	Max int
	// now is time.Now, overridable in tests.
	now func() time.Time
}

func (c *Channels) max() int {
	if c.Max > 0 {
		return c.Max
	}
	return maxChannelWidth
}

// NewChannels builds an empty registry. A model is created on first use, so
// nothing needs to know the model layout in advance — which is the property that
// makes a changing layout a non-event.
func NewChannels() *Channels {
	return &Channels{m: map[string]*modelChannel{}, now: time.Now}
}

type modelChannel struct {
	cond     *sync.Cond
	width    int
	inFlight int
	clean    int       // consecutive successes since the last width change
	chillEnd time.Time // no growth before this
	// consec429 counts backpressure events not yet separated by a settled
	// stretch, and lengthens the cooldown. Without it a model that genuinely
	// serves one slot is a probe loop forever: grow to 2, 429, fall to 1, wait,
	// grow to 2 again.
	consec429 int
	// Peak/total are for the report, not the control law.
	peakInFlight int
	calls        int
	n429         int
}

func (c *Channels) get(model string) *modelChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.m[model]
	if ch == nil {
		ch = &modelChannel{cond: sync.NewCond(&sync.Mutex{}), width: defaultChannelWidth}
		c.m[model] = ch
	}
	return ch
}

// Acquire takes a slot for one call to model, blocking until one is free or ctx
// ends. The returned release must be called exactly once.
//
// release takes the call's outcome: ok=false for a call that failed for any
// reason other than backpressure. Backpressure is reported separately by
// Note429, because agentkit RETRIES a 429 internally — the call that eventually
// succeeds still met backpressure, and a controller that only looked at final
// outcomes would never see it.
func (c *Channels) Acquire(ctx context.Context, model string) (release func(ok bool), err error) {
	if model == "" {
		return func(bool) {}, nil // nothing to key on; do not gate
	}
	ch := c.get(model)

	ch.cond.L.Lock()
	// A waiter must be woken when ctx ends, and sync.Cond has no deadline. One
	// watchdog per wait, torn down on the way out.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			ch.cond.Broadcast()
		case <-done:
		}
	}()
	for ch.inFlight >= ch.width {
		if ctx.Err() != nil {
			ch.cond.L.Unlock()
			return nil, ctx.Err()
		}
		ch.cond.Wait()
	}
	if ctx.Err() != nil {
		ch.cond.L.Unlock()
		return nil, ctx.Err()
	}
	ch.inFlight++
	ch.calls++
	if ch.inFlight > ch.peakInFlight {
		ch.peakInFlight = ch.inFlight
	}
	ch.cond.L.Unlock()

	var once sync.Once
	return func(ok bool) {
		once.Do(func() {
			ch.cond.L.Lock()
			ch.inFlight--
			if ok {
				ch.clean++
				// Additive increase, scaled by the current width so a wide
				// channel earns its next slot more slowly than a narrow one.
				// Never during a chill: the point of backing off is to stay
				// backed off long enough for the other traffic to clear.
				if ch.clean >= growAfterCleanCalls*ch.width &&
					ch.width < c.max() && !c.now().Before(ch.chillEnd) {
					ch.width++
					ch.clean = 0
					// The chill elapsed and the model took a full width of clean
					// calls, so the last backpressure is no longer the current
					// situation. Forget the streak, or a model narrowed once in
					// the morning re-probes on an hours-long cooldown all day.
					ch.consec429 = 0
				}
			} else {
				// A plain failure is not backpressure and must not widen the
				// channel, but it is not evidence to narrow it either — a
				// document that is too large fails at any width.
				ch.clean = 0
			}
			ch.cond.L.Unlock()
			ch.cond.Broadcast()
		})
	}, nil
}

// Note429 records backpressure for a model: halve the width and refuse to grow
// for a cooldown.
//
// Multiplicative decrease against additive increase, which is the pairing that
// converges when several clients share one resource — and raglit IS one of
// several. Halving on the first 429 rather than after a few is deliberate: the
// server has already queued or rejected the call by the time it says this, so
// the cost of over-asking has been paid.
func (c *Channels) Note429(model string) {
	if model == "" {
		return
	}
	ch := c.get(model)
	ch.cond.L.Lock()
	ch.n429++
	ch.consec429++
	if w := ch.width / 2; w >= minChannelWidth {
		ch.width = w
	} else {
		ch.width = minChannelWidth
	}
	ch.clean = 0
	ch.chillEnd = c.now().Add(chillFor(ch.consec429))
	ch.cond.L.Unlock()
	// Narrowing can only make waiters wait; broadcasting keeps the loop honest
	// rather than leaving one parked against a width that already changed.
	ch.cond.Broadcast()
}

// Observe returns an llm.Client.OnRetry hook for model that feeds backpressure
// into the controller and then chains to next (which may be nil).
//
// Chained rather than replacing, because the RetryTally is already on that hook
// and its record — what a job had to survive — is what the health report reads.
func (c *Channels) Observe(model string, next func(llm.RetryEvent)) func(llm.RetryEvent) {
	return func(e llm.RetryEvent) {
		if e.Kind == llm.Retry429 {
			c.Note429(model)
		}
		if next != nil {
			next(e)
		}
	}
}

// ChannelStat is one model's admission channel, for the status report.
type ChannelStat struct {
	Model    string `json:"model"`
	Width    int    `json:"width"`
	InFlight int    `json:"in_flight"`
	Peak     int    `json:"peak"`
	Calls    int    `json:"calls"`
	N429     int    `json:"n_429"`
	// Chilled is true while backpressure is still holding the width down.
	Chilled bool `json:"chilled,omitempty"`
}

// Stats reports every model this process has called, widest first. Reported
// because the width is now a LEARNED number rather than a configured one, and a
// learned number nobody can see is indistinguishable from a bug.
func (c *Channels) Stats() []ChannelStat {
	c.mu.Lock()
	models := make([]string, 0, len(c.m))
	for name := range c.m {
		models = append(models, name)
	}
	chans := make([]*modelChannel, 0, len(models))
	for _, name := range models {
		chans = append(chans, c.m[name])
	}
	c.mu.Unlock()

	out := make([]ChannelStat, 0, len(models))
	for i, name := range models {
		ch := chans[i]
		ch.cond.L.Lock()
		out = append(out, ChannelStat{
			Model: name, Width: ch.width, InFlight: ch.inFlight, Peak: ch.peakInFlight,
			Calls: ch.calls, N429: ch.n429, Chilled: c.now().Before(ch.chillEnd),
		})
		ch.cond.L.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// Gate wraps a Chatter so every completion takes a slot in model's channel.
//
// Chatter and VectorClient are the only two seams raglit calls a model through —
// OCR, segmentation and identity all take a Chatter; embedding takes a
// VectorClient — so gating these two covers every model call in the process
// without touching any of the four call sites.
func (c *Channels) Gate(inner Chatter, model string) Chatter {
	if inner == nil || model == "" {
		return inner
	}
	return &gatedChatter{inner: inner, ch: c, model: model}
}

type gatedChatter struct {
	inner Chatter
	ch    *Channels
	model string
}

// ChatStream holds the slot until the STREAM is drained, not until ChatStream
// returns.
//
// The distinction is the whole call: ChatStream returns as soon as the response
// headers arrive and the generation runs for minutes afterwards, so releasing on
// return would let a model with one slot run twenty transcriptions at once and
// leave the width meaning nothing.
func (g *gatedChatter) ChatStream(ctx context.Context, messages []llm.Message,
	tools []llm.ToolDef, opts *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	release, err := g.ch.Acquire(ctx, g.model)
	if err != nil {
		return nil, err
	}
	in, err := g.inner.ChatStream(ctx, messages, tools, opts)
	if err != nil {
		release(false)
		return nil, err
	}
	out := make(chan llm.StreamChunk)
	go func() {
		ok := true
		defer func() {
			release(ok)
			close(out)
		}()
		for chunk := range in {
			if chunk.Error != "" {
				ok = false
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				// The consumer is gone. Keep draining the source so the slot is
				// released when the generation actually ends rather than while
				// it is still producing — an abandoned stream still occupies the
				// model until the transport tears it down.
				ok = false
				for range in {
				}
				return
			}
		}
	}()
	return out, nil
}

// GateVector wraps an embedding client the same way. Embed is synchronous, so
// the slot spans the call exactly.
func (c *Channels) GateVector(inner VectorClient, model string) VectorClient {
	if inner == nil || model == "" {
		return inner
	}
	return &gatedVector{inner: inner, ch: c, model: model}
}

type gatedVector struct {
	inner VectorClient
	ch    *Channels
	model string
}

func (g *gatedVector) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	// Keyed on the model actually being called when the caller names one, so a
	// client reused for a second model does not spend the first one's slots.
	name := model
	if name == "" {
		name = g.model
	}
	release, err := g.ch.Acquire(ctx, name)
	if err != nil {
		return nil, err
	}
	v, err := g.inner.Embed(ctx, model, input)
	release(err == nil)
	return v, err
}
