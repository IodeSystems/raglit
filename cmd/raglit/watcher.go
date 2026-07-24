package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/iodesystems/raglit"
)

// Directory watching (daemon-side, opt-in per project via config "watch": true).
//
// A client registers its project home; the daemon then keeps that project's
// configured source roots fresh — on an interval it re-reads the project config,
// re-plans the sources (same include/ignore/.gitignore as `sync`), and enqueues
// files whose mtime changed (new or modified) into the project's namespaced
// indexes, and removes documents whose source file disappeared. The content-hash
// dedup means an unchanged file that slips through is a no-op, so the poll is
// cheap. Registrations persist to <root>/watches.json and reload on startup.
//
// Poll (not fsnotify) on purpose: no recursive-watch bookkeeping, it naturally
// catches deletes and offline changes, and re-reading the config each tick picks
// up edits to roots/rules with no re-registration.

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
	seen  map[string]map[string]fileState // home → (abs file → last state)
}

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
	w.scanHome(abs)
	return nil
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
	return nil
}

type watchInfo struct {
	Home     string `json:"home"`
	Project  string `json:"project"`
	Watching bool   `json:"watching"` // config still has watch:true
	Files    int    `json:"files"`
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

// run scans all registered homes once, then every interval until ctx is done.
func (w *watcher) run(ctx context.Context) {
	w.scanAll()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.scanAll()
		}
	}
}

func (w *watcher) scanAll() {
	w.mu.Lock()
	homes := make([]string, 0, len(w.homes))
	for h := range w.homes {
		homes = append(homes, h)
	}
	w.mu.Unlock()
	for _, h := range homes {
		w.scanHome(h)
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

	w.mu.Lock()
	defer w.mu.Unlock()
	prev := w.seen[home]
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
			_ = store.DeleteDocument(f) // source gone → drop the doc
		}
		delete(prev, f)
		rm++
	}
	w.seen[home] = prev
	if enq > 0 || rm > 0 {
		fmt.Fprintf(os.Stderr, "raglit watch %s: +%d changed, -%d removed\n", ns, enq, rm)
	}
	return enq, rm
}
