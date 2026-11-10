package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iodesystems/raglit"
)

// Directory watching (daemon-side, opt-in per project via config "watch": true).
//
// A client registers its project home; the daemon then keeps that project's
// configured source roots fresh. Each root is watched through a shared
// raglit.WatchHub (fsnotify, debounced, one watcher per directory), and each
// burst of changes runs raglit.IndexTree over the changed paths: new and
// changed files are queued, documents whose files are gone are removed.
// Registrations persist to <root>/watches.json and reload on startup.
//
// On every interval the daemon re-reads the configs, so an edit to roots or
// rules changes what is watched with no re-registration. Polling remains as
// the fallback for a root whose watcher hit the OS watch limit (Degraded):
// its owner is then reconciled by mtime on every interval, as before.

type fileState struct {
	mtime int64
	index string // daemon index this file was enqueued into (for delete routing)
}

type watcher struct {
	reg       *raglit.Registry
	interval  time.Duration
	statePath string

	mu    sync.Mutex
	homes map[string]bool                 // registered project homes (abs .raglit paths)
	dirs  []dirWatch                      // directories watched into an index
	seen  map[string]map[string]fileState // home or dirWatch.key → (abs file → last state)

	// refreshMu serializes refresh, and run's teardown with it: two refreshes
	// at once could each see a target added or gone, and a subscription made
	// beside an unsubscribe would never be ended.
	refreshMu sync.Mutex

	// Event watching, set up by run. Nil hub: no watching (tests, and before
	// run), and Add/AddDir index synchronously only.
	ctx      context.Context
	hub      *raglit.WatchHub
	subs     map[string]*rootWatch // rootWatch.key → subscription
	degraded map[string]bool       // owners (home or dirWatch.key) that poll

	fullPasses atomic.Int64 // whole-tree IndexTree runs, for tests
}

// rootWatch is one tree target's subscription and its pending changes.
type rootWatch struct {
	key   string // owner + target signature
	owner string // home, or dirWatch.key()
	t     treeTarget
	unsub func()

	mu    sync.Mutex
	dirty map[string]bool
	full  bool
}

func (rw *rootWatch) mark(c raglit.Changes) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if len(c.Paths) == 0 {
		rw.full = true
		return
	}
	if rw.dirty == nil {
		rw.dirty = map[string]bool{}
	}
	for _, p := range c.Paths {
		rw.dirty[p] = true
	}
}

// take drains what is pending: (nil, true) is the whole tree.
func (rw *rootWatch) take() (paths []string, any bool) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.full {
		rw.full, rw.dirty = false, nil
		return nil, true
	}
	for p := range rw.dirty {
		paths = append(paths, p)
	}
	rw.dirty = nil
	sort.Strings(paths)
	return paths, len(paths) > 0
}

// dirWatch is a directory the daemon keeps one index fresh from. Unlike a home
// it needs no project config, so a client with only a URL and a key can make
// one. Persisted to <root>/watch-dirs.json.
type dirWatch struct {
	Index string `json:"index"`
	Dir   string `json:"dir"`
}

func (d dirWatch) key() string { return "dir:" + d.Index + ":" + d.Dir }

func newWatcher(reg *raglit.Registry, root string, interval time.Duration) *watcher {
	return &watcher{
		reg:       reg,
		interval:  interval,
		statePath: filepath.Join(root, "watches.json"),
		homes:     map[string]bool{},
		seen:      map[string]map[string]fileState{},
	}
}

// load reads persisted registrations (a JSON array of home paths).
func (w *watcher) load() {
	if b, err := os.ReadFile(w.dirsPath()); err == nil {
		w.mu.Lock()
		_ = json.Unmarshal(b, &w.dirs)
		w.mu.Unlock()
	}
	b, err := os.ReadFile(w.statePath)
	if err != nil {
		return
	}
	var homes []string
	if json.Unmarshal(b, &homes) != nil {
		return
	}
	w.mu.Lock()
	for _, h := range homes {
		w.homes[h] = true
	}
	w.mu.Unlock()
}

// persist writes the current registrations (caller holds no lock).
func (w *watcher) persist() {
	w.mu.Lock()
	homes := make([]string, 0, len(w.homes))
	for h := range w.homes {
		homes = append(homes, h)
	}
	w.mu.Unlock()
	sort.Strings(homes)
	if b, err := json.MarshalIndent(homes, "", "  "); err == nil {
		_ = os.WriteFile(w.statePath, b, 0o644)
	}
}

// Add registers a project home (absolutized) and scans it immediately.
func (w *watcher) Add(home string) error {
	abs, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	if _, ok, err := raglit.LoadConfig(raglit.Home(abs)); err != nil || !ok {
		return fmt.Errorf("no config at %s", abs)
	}
	w.mu.Lock()
	w.homes[abs] = true
	w.mu.Unlock()
	w.persist()
	w.scanOwner(abs, w.refresh())
	return nil
}

func (w *watcher) dirsPath() string {
	return filepath.Join(filepath.Dir(w.statePath), "watch-dirs.json")
}

func (w *watcher) persistDirsLocked() error {
	b, err := json.MarshalIndent(w.dirs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(w.dirsPath(), b, 0o600)
}

// AddDir watches dir into index and scans it now. Adding one again is a no-op.
func (w *watcher) AddDir(index, dir string) error {
	d := dirWatch{Index: index, Dir: dir}
	w.mu.Lock()
	if !slices.Contains(w.dirs, d) {
		w.dirs = append(w.dirs, d)
		if err := w.persistDirsLocked(); err != nil {
			w.mu.Unlock()
			return err
		}
	}
	w.mu.Unlock()
	w.scanOwner(d.key(), w.refresh())
	return nil
}

// RemoveDir stops watching. What was indexed stays.
func (w *watcher) RemoveDir(index, dir string) error {
	d := dirWatch{Index: index, Dir: dir}
	w.mu.Lock()
	defer w.mu.Unlock()
	i := slices.Index(w.dirs, d)
	if i < 0 {
		return fmt.Errorf("%s is not watched into %s", dir, index)
	}
	w.dirs = slices.Delete(w.dirs, i, i+1)
	delete(w.seen, d.key())
	defer w.refreshAfter()
	return w.persistDirsLocked()
}

// RemoveIndex stops every watch into index. Left in place, the next scan would
// recreate an index that was just deleted.
func (w *watcher) RemoveIndex(index string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	kept := w.dirs[:0:0]
	for _, d := range w.dirs {
		if d.Index == index {
			delete(w.seen, d.key())
			continue
		}
		kept = append(kept, d)
	}
	if len(kept) == len(w.dirs) {
		return nil
	}
	w.dirs = kept
	defer w.refreshAfter()
	return w.persistDirsLocked()
}

// Dirs is the directories watched into any index.
func (w *watcher) Dirs() []dirWatch {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.dirs)
}

// scanDir plans one watched directory with the same include, ignore and
// .gitignore rules as a project root.
func (w *watcher) scanDir(d dirWatch) (int, int) {
	// A project whose keys are all revoked stops reading the host: the grant
	// that allowed this watch is gone. It resumes if a key is minted again.
	owner := w.reg.ACLOf(d.Index).Owner
	if ns, ok := strings.CutPrefix(owner, raglit.ProjectPrincipalPrefix); ok && !w.reg.Keys().HasProject(ns) {
		return 0, 0
	}
	if _, err := w.reg.Existing(d.Index); err != nil {
		return 0, 0 // deleted: a directory watch does not make the index again
	}
	cfg := raglit.Config{Indexes: map[string]raglit.IndexConfig{d.Index: {Roots: []raglit.Root{{Path: d.Dir}}}}}
	plan, err := raglit.PlanSources(cfg, d.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit watch %s: plan: %v\n", d.Dir, err)
		return 0, 0
	}
	real, err := filepath.EvalSymlinks(d.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit watch %s: %v\n", d.Dir, err)
		return 0, 0
	}
	current := map[string]string{}
	for _, f := range plan[d.Index] {
		// A key is granted a directory, so a link out of it is not watched.
		if r, err := filepath.EvalSymlinks(f); err == nil && strings.HasPrefix(r, real+string(filepath.Separator)) {
			current[f] = d.Index
		}
	}
	return w.reconcile(d.key(), d.Dir, current)
}

// Remove unregisters a project home.
func (w *watcher) Remove(home string) error {
	abs, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	w.mu.Lock()
	delete(w.homes, abs)
	delete(w.seen, abs)
	w.mu.Unlock()
	w.persist()
	w.refresh()
	return nil
}

type watchInfo struct {
	Home     string `json:"home"`
	Project  string `json:"project"`
	Watching bool   `json:"watching"` // config still has watch:true
	Files    int    `json:"files"`
}

// registered is the registered project homes.
func (w *watcher) registered() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	homes := make([]string, 0, len(w.homes))
	for h := range w.homes {
		homes = append(homes, h)
	}
	sort.Strings(homes)
	return homes
}

// List reports the registered homes with their project + live file count.
func (w *watcher) List() []watchInfo {
	w.mu.Lock()
	homes := make([]string, 0, len(w.homes))
	for h := range w.homes {
		homes = append(homes, h)
	}
	w.mu.Unlock()
	sort.Strings(homes)
	out := make([]watchInfo, 0, len(homes))
	for _, h := range homes {
		wi := watchInfo{Home: h}
		cfg, ok, _ := raglit.LoadConfig(raglit.Home(h))
		if ok {
			wi.Project = cfg.Project
			wi.Watching = cfg.Watch
			if plan, err := raglit.PlanSources(cfg, filepath.Dir(h)); err == nil {
				for _, fs := range plan {
					wi.Files += len(fs)
				}
			}
		}
		out = append(out, wi)
	}
	return out
}

// run watches every registered home and directory until ctx is done. Each
// interval it re-reads the configs (refresh) and polls the owners whose
// watchers are Degraded. It returns once the hub's watchers, callbacks and
// index runs have ended, so a caller that waits for run may then close the
// registry.
func (w *watcher) run(ctx context.Context) {
	w.mu.Lock()
	w.ctx = ctx
	w.hub = raglit.NewWatchHub(raglit.WatchOptions{})
	w.mu.Unlock()
	defer func() {
		w.refreshMu.Lock()
		w.mu.Lock()
		hub := w.hub
		w.hub, w.subs = nil, nil
		w.mu.Unlock()
		hub.Stop()
		w.refreshMu.Unlock()
		hub.Wait() // outside refreshMu: nothing the hub runs may wait on it
	}()
	w.refresh()
	interval := w.interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.refresh()
			w.pollDegraded()
		}
	}
}

// targets is every tree target the registrations ask for, by owner.
func (w *watcher) targets() map[string][]treeTarget {
	w.mu.Lock()
	homes := make([]string, 0, len(w.homes))
	for h := range w.homes {
		homes = append(homes, h)
	}
	dirs := slices.Clone(w.dirs)
	w.mu.Unlock()
	out := map[string][]treeTarget{}
	for _, h := range homes {
		if ts := homeTargets(h); len(ts) > 0 {
			out[h] = ts
		}
	}
	for _, d := range dirs {
		if t, err := dirTarget(d); err == nil {
			out[d.key()] = append(out[d.key()], t)
		}
	}
	return out
}

// homeTargets is a registered home's targets; none when its config is gone
// or has watch turned off.
func homeTargets(home string) []treeTarget {
	cfg, ok, err := raglit.LoadConfig(raglit.Home(home))
	if err != nil || !ok || !cfg.Watch || cfg.Project == "" {
		return nil
	}
	return configTargets(cfg, filepath.Dir(home), raglit.NormalizeIndexName(cfg.Project), "")
}

// refresh makes the subscriptions match the registrations: a new target is
// subscribed and indexed in full, a target no longer asked for (a config
// edit, an unwatch) is unsubscribed. A no-op before run. It returns the
// subscriptions it added.
func (w *watcher) refresh() []*rootWatch {
	w.refreshMu.Lock()
	defer w.refreshMu.Unlock()
	want := w.targets()
	w.mu.Lock()
	if w.hub == nil {
		w.mu.Unlock()
		return nil
	}
	if w.subs == nil {
		w.subs = map[string]*rootWatch{}
	}
	keep := map[string]bool{}
	var added []*rootWatch
	for owner, ts := range want {
		for _, t := range ts {
			key := owner + "\x00" + t.sig
			keep[key] = true
			if w.subs[key] != nil {
				continue
			}
			rw := &rootWatch{key: key, owner: owner, t: t}
			w.subs[key] = rw
			added = append(added, rw)
		}
	}
	var gone []*rootWatch
	for key, rw := range w.subs {
		if !keep[key] {
			gone = append(gone, rw)
			delete(w.subs, key)
		}
	}
	hub := w.hub
	w.mu.Unlock()
	for _, rw := range gone {
		if rw.unsub != nil {
			rw.unsub()
		}
	}
	for _, rw := range added {
		unsub, err := hub.Subscribe(rw.t.dir, rw.t.rules, func(c raglit.Changes) {
			if c.Degraded {
				w.setDegraded(rw.owner)
			}
			rw.mark(c)
			go w.indexWatch(rw)
		})
		if err != nil {
			// A root that is not there (yet): polled until it is.
			fmt.Fprintf(os.Stderr, "raglit watch %s: %v\n", rw.t.dir, err)
			w.mu.Lock()
			delete(w.subs, rw.key)
			w.mu.Unlock()
			continue
		}
		rw.unsub = unsub
		rw.mark(raglit.Changes{}) // a new subscription starts from the whole tree
		go w.indexWatch(rw)
	}
	return added
}

// refreshAfter is refresh for a caller holding w.mu: it runs once the
// caller's deferred unlock has.
func (w *watcher) refreshAfter() { go w.refresh() }

func (w *watcher) setDegraded(owner string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.degraded == nil {
		w.degraded = map[string]bool{}
	}
	w.degraded[owner] = true
}

// pollDegraded reconciles, by mtime, the owners whose watchers stopped
// adding watches.
func (w *watcher) pollDegraded() {
	w.mu.Lock()
	var homes []string
	var dirs []dirWatch
	for owner := range w.degraded {
		if w.homes[owner] {
			homes = append(homes, owner)
		}
	}
	for _, d := range w.dirs {
		if w.degraded[d.key()] {
			dirs = append(dirs, d)
		}
	}
	w.mu.Unlock()
	for _, h := range homes {
		w.scanHome(h)
	}
	for _, d := range dirs {
		w.scanDir(d)
	}
}

// indexWatch runs what rw has pending, one run per target at a time: bursts
// that arrive during a run share one follow-up, which drains them all.
func (w *watcher) indexWatch(rw *rootWatch) {
	w.mu.Lock()
	hub, ctx := w.hub, w.ctx
	w.mu.Unlock()
	if hub == nil {
		return
	}
	_, _ = hub.Index(ctx, rw.key, func(ctx context.Context) (raglit.TreeResult, error) {
		paths, any := rw.take()
		w.mu.Lock()
		live := w.subs[rw.key] == rw
		w.mu.Unlock()
		if !any || !live {
			return raglit.TreeResult{}, nil // nothing pending, or unwatched since
		}
		return w.indexTarget(ctx, rw.owner, rw.t, paths)
	})
}

// scanOwner indexes every target of one owner in full and returns when that
// is done: one pass per target. With the hub, it goes through the target's
// hub.Index flight, so it shares the full pass a new subscription (added, by
// refresh) already asked for; an existing subscription is asked for one.
// Before run, it indexes directly.
func (w *watcher) scanOwner(owner string, added []*rootWatch) {
	w.mu.Lock()
	hub := w.hub
	var rws []*rootWatch
	for _, rw := range w.subs {
		if rw.owner == owner {
			rws = append(rws, rw)
		}
	}
	w.mu.Unlock()
	if hub == nil {
		w.indexOwner(owner)
		return
	}
	for _, rw := range rws {
		if !slices.Contains(added, rw) {
			rw.mark(raglit.Changes{})
		}
		w.indexWatch(rw)
	}
}

// indexOwner indexes every target of one owner in full, now.
func (w *watcher) indexOwner(owner string) {
	ctx := context.Background()
	for _, t := range w.targets()[owner] {
		_, _ = w.indexTarget(ctx, owner, t, nil)
	}
}

// indexTarget runs IndexTree for one target over paths (nil = the whole
// tree), and logs what changed.
func (w *watcher) indexTarget(ctx context.Context, owner string, t treeTarget, paths []string) (raglit.TreeResult, error) {
	// A project whose keys are all revoked stops reading the host: the grant
	// that allowed a directory watch is gone. It resumes if a key is minted.
	get := w.reg.Get
	if strings.HasPrefix(owner, "dir:") {
		o := w.reg.ACLOf(t.index).Owner
		if ns, ok := strings.CutPrefix(o, raglit.ProjectPrincipalPrefix); ok && !w.reg.Keys().HasProject(ns) {
			return raglit.TreeResult{}, nil
		}
		// A directory watch feeds an index that exists; it does not make one.
		// The index deleted while a burst or pass of this target is in
		// flight (RemoveIndex unsubscribes after DeleteAs, asynchronously)
		// stays deleted.
		get = w.reg.Existing
	}
	st, err := get(t.index)
	if err != nil {
		return raglit.TreeResult{}, err
	}
	if paths == nil {
		w.fullPasses.Add(1)
	}
	var all []treeTarget
	for _, ts := range w.targets() {
		all = append(all, ts...)
	}
	res, err := raglit.IndexTree(ctx, st, t.dir, raglit.TreeOptions{Rules: t.rules, Paths: paths, Keep: keepFor(t, all)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit watch %s → %s: %v\n", t.dir, t.index, err)
		return res, err
	}
	if treeChanged(res) {
		fmt.Fprintf(os.Stderr, "raglit watch %s → %s: %s\n", t.dir, t.index, treeLine(res))
	}
	return res, nil
}

func (w *watcher) scanAll() {
	w.mu.Lock()
	homes := make([]string, 0, len(w.homes))
	for h := range w.homes {
		homes = append(homes, h)
	}
	dirs := slices.Clone(w.dirs)
	w.mu.Unlock()
	for _, h := range homes {
		w.scanHome(h)
	}
	for _, d := range dirs {
		w.scanDir(d)
	}
}

// scanHome re-plans one project and reconciles: enqueue new/modified files,
// delete documents whose source file is gone. Returns (enqueued, removed).
func (w *watcher) scanHome(home string) (int, int) {
	cfg, ok, err := raglit.LoadConfig(raglit.Home(home))
	if err != nil || !ok || !cfg.Watch || cfg.Project == "" {
		return 0, 0 // watch turned off / unconfigured → leave it registered, do nothing
	}
	ns := raglit.NormalizeIndexName(cfg.Project)
	plan, err := raglit.PlanSources(cfg, filepath.Dir(home))
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit watch %s: plan: %v\n", ns, err)
		return 0, 0
	}
	// current: abs file → daemon index it belongs to.
	current := map[string]string{}
	for key, files := range plan {
		di := nsIndex(ns, key)
		for _, f := range files {
			current[f] = di
		}
	}
	return w.reconcile(home, ns, current)
}

// reconcile enqueues what is new or changed in current (abs file → daemon
// index) and removes the documents of files that are gone.
func (w *watcher) reconcile(key, label string, current map[string]string) (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	prev := w.seen[key]
	if prev == nil {
		prev = map[string]fileState{}
	}
	var enq, rm int
	for f, di := range current {
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		mt := fi.ModTime().UnixNano()
		if st, ok := prev[f]; ok && st.mtime == mt && st.index == di {
			continue // unchanged
		}
		st, err := w.reg.Get(di)
		if err != nil {
			continue
		}
		if _, err := st.Enqueue(f, ""); err != nil {
			continue
		}
		prev[f] = fileState{mtime: mt, index: di}
		enq++
	}
	for f, st := range prev {
		if _, still := current[f]; still {
			continue
		}
		if store, err := w.reg.Get(st.index); err == nil {
			_ = store.RemoveDocument(f, false) // source gone → drop the doc
		}
		delete(prev, f)
		rm++
	}
	w.seen[key] = prev
	if enq > 0 || rm > 0 {
		fmt.Fprintf(os.Stderr, "raglit watch %s: +%d changed, -%d removed\n", label, enq, rm)
	}
	return enq, rm
}
