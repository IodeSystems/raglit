package raglit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// WatchHub shares watchers and index runs across callers (R12e): one
// TreeWatcher per root however many subscribe, and one index run per root at
// a time.
type WatchHub struct {
	opts WatchOptions
	// life ends at Stop: index runs are cancelled by it, not by a caller.
	life context.Context
	end  context.CancelFunc
	runs sync.WaitGroup

	mu      sync.Mutex
	stopped bool
	roots   map[string]*hubRoot
	flights map[string]*hubFlights
}

var errHubStopped = errors.New("raglit: watch hub stopped")

type hubRoot struct {
	w    *TreeWatcher
	subs map[int]*hubSub
	next int
}

type hubSub struct {
	rules TreeRules
	fn    func(Changes)
	// mu is held while fn runs; unsubscribe takes it to wait out a call in
	// progress. dead: unsubscribed, deliver nothing more.
	mu   sync.Mutex
	dead atomic.Bool
}

// deliver calls fn unless s was unsubscribed.
func (s *hubSub) deliver(c Changes) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dead.Load() {
		s.fn(c)
	}
}

// NewWatchHub makes a hub whose watchers use o.
func NewWatchHub(o WatchOptions) *WatchHub {
	life, end := context.WithCancel(context.Background())
	return &WatchHub{opts: o, life: life, end: end, roots: map[string]*hubRoot{}, flights: map[string]*hubFlights{}}
}

// Subscribe calls fn with every burst under root. The first subscriber of a
// root starts its watcher; the watcher skips only what the built-in rules and
// that subscriber's Gitignore setting skip, and each subscriber's Ignore,
// Include and Skip then filter the paths it is given (a burst left with no
// paths is not delivered, unless it is a whole-tree or Degraded one). The
// last unsubscribe stops the watcher. Once unsubscribe returns, fn is not
// called again (unsubscribe from inside fn returns at once; fn gets no
// later burst). A watcher that idles out (WatchOptions Idle) drops its
// subscribers; the next Subscribe starts a new one.
func (h *WatchHub) Subscribe(root string, r TreeRules, fn func(Changes)) (unsubscribe func(), err error) {
	key, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	key = filepath.Clean(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return nil, errHubStopped
	}
	hr := h.roots[key]
	if hr == nil {
		hr = &hubRoot{subs: map[int]*hubSub{}}
		w, err := watchTree(context.Background(), key, TreeRules{Gitignore: r.Gitignore}, h.opts,
			func(c Changes) { h.fanout(key, hr, c) },
			func() { h.dropRoot(key, hr) })
		if err != nil {
			return nil, err
		}
		hr.w = w
		h.roots[key] = hr
	}
	id := hr.next
	hr.next++
	sub := &hubSub{rules: r, fn: fn}
	hr.subs[id] = sub
	var once sync.Once
	return func() {
		once.Do(func() {
			sub.dead.Store(true)
			h.mu.Lock()
			delete(hr.subs, id)
			var stop *TreeWatcher
			if len(hr.subs) == 0 && h.roots[key] == hr {
				delete(h.roots, key)
				stop = hr.w
			}
			h.mu.Unlock()
			if stop != nil {
				stop.Stop()
			}
			if !hr.w.onLoop() {
				sub.mu.Lock() // wait out a delivery in progress
				sub.mu.Unlock()
			}
		})
	}, nil
}

func (h *WatchHub) dropRoot(key string, hr *hubRoot) {
	h.mu.Lock()
	if h.roots[key] == hr {
		delete(h.roots, key)
	}
	h.mu.Unlock()
}

// fanout delivers c to each subscriber of hr, filtered by its rules.
func (h *WatchHub) fanout(root string, hr *hubRoot, c Changes) {
	h.mu.Lock()
	subs := make([]*hubSub, 0, len(hr.subs))
	for _, s := range hr.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		sc := c
		if len(c.Paths) > 0 {
			sc.Paths = nil
			for _, p := range c.Paths {
				if subAdmits(root, s.rules, p) {
					sc.Paths = append(sc.Paths, p)
				}
			}
			if len(sc.Paths) == 0 && !c.Degraded {
				continue
			}
		}
		s.deliver(sc)
	}
}

// subAdmits applies a subscriber's rules to one reported path. Include is
// applied only to a path that is a file now: a path that is gone or a
// directory may hold what Include selects.
func subAdmits(root string, r TreeRules, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if r.excluded(p, rel) {
		return false
	}
	if len(r.Include) == 0 {
		return true
	}
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return matchAny(r.Include, rel)
	}
	return true
}

// watcher is root's current watcher, nil when none (tests).
func (h *WatchHub) watcher(root string) *TreeWatcher {
	h.mu.Lock()
	defer h.mu.Unlock()
	if hr := h.roots[filepath.Clean(root)]; hr != nil {
		return hr.w
	}
	return nil
}

// Stop stops every watcher, cancels the index runs in flight and waits for
// them. Subscriptions end; a later Subscribe or Index fails. Stop must not be
// called from inside an Index do.
func (h *WatchHub) Stop() {
	h.mu.Lock()
	h.stopped = true
	ws := make([]*TreeWatcher, 0, len(h.roots))
	for k, hr := range h.roots {
		ws = append(ws, hr.w)
		delete(h.roots, k)
	}
	h.mu.Unlock()
	h.end()
	for _, w := range ws {
		w.Stop()
	}
	h.runs.Wait()
}

// hubFlights is one key's index runs: whether one is running, and the single
// follow-up that requests arriving during it share.
type hubFlights struct {
	next   *hubFlight
	nextDo func(context.Context) (TreeResult, error)
	ctx    context.Context
}

type hubFlight struct {
	done chan struct{}
	res  TreeResult
	err  error
}

// Index runs do for key (a root, or any string naming one tree), one run at
// a time. A call while no run is going starts one and gets its result. A call
// during a run joins the one follow-up run that starts when it ends: every
// call that arrives during the same run shares that follow-up and its result,
// and the follow-up runs the do of the last of them. So do must cover
// everything asked so far (drain a set of pending paths, say), not only its
// own caller's request. A caller whose ctx ends stops waiting; the run goes
// on, under a context that keeps ctx's values and is cancelled only by Stop.
func (h *WatchHub) Index(ctx context.Context, key string, do func(context.Context) (TreeResult, error)) (TreeResult, error) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return TreeResult{}, errHubStopped
	}
	hf, running := h.flights[key]
	var f *hubFlight
	if !running {
		hf = &hubFlights{}
		h.flights[key] = hf
		f = &hubFlight{done: make(chan struct{})}
		h.runs.Add(1)
		h.mu.Unlock()
		go h.run(key, hf, f, do, context.WithoutCancel(ctx))
	} else {
		if hf.next == nil {
			hf.next = &hubFlight{done: make(chan struct{})}
		}
		hf.nextDo, hf.ctx = do, context.WithoutCancel(ctx)
		f = hf.next
		h.mu.Unlock()
	}
	select {
	case <-f.done:
		return f.res, f.err
	case <-ctx.Done():
		return TreeResult{}, ctx.Err()
	}
}

func (h *WatchHub) run(key string, hf *hubFlights, f *hubFlight, do func(context.Context) (TreeResult, error), ctx context.Context) {
	defer h.runs.Done()
	for {
		f.res, f.err = h.runOne(ctx, do)
		close(f.done)
		h.mu.Lock()
		if hf.next == nil {
			delete(h.flights, key)
			h.mu.Unlock()
			return
		}
		f, do, ctx = hf.next, hf.nextDo, hf.ctx
		hf.next, hf.nextDo, hf.ctx = nil, nil, nil
		h.mu.Unlock()
	}
}

// runOne runs do under ctx's values and the hub's lifetime.
func (h *WatchHub) runOne(ctx context.Context, do func(context.Context) (TreeResult, error)) (TreeResult, error) {
	if err := h.life.Err(); err != nil {
		return TreeResult{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(h.life, cancel)()
	return do(ctx)
}
