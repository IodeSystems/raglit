package main

import (
	"context"
	"strings"
	"sync"

	"github.com/iodesystems/raglit"
)

// qualityLogRegistry owns the daemon's per-project quality log writers.
// Created once by the daemon (httpd.go) and handed to the watcher, which
// resolves a project namespace to its home (the registration list).
// Writer(ns) returns the project's writer, creating it lazily on first
// search, or nil when the log is off (the default) or the home is unknown.
// Close shuts every open writer down at daemon shutdown.
type qualityLogRegistry struct {
	mu           sync.Mutex
	writers      map[string]*raglit.QualityLogWriter
	homeResolver func(ns string) (raglit.Home, bool)
}

func newQualityLogRegistry() *qualityLogRegistry {
	return &qualityLogRegistry{writers: map[string]*raglit.QualityLogWriter{}}
}

func (r *qualityLogRegistry) setHomeResolver(fn func(ns string) (raglit.Home, bool)) {
	r.mu.Lock()
	r.homeResolver = fn
	r.mu.Unlock()
}

// Writer returns the project's writer, creating it on first use, or nil
// when the log is off (the default), the home is unresolvable, or the file
// cannot be opened.
func (r *qualityLogRegistry) Writer(ns string) *raglit.QualityLogWriter {
	if ns == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.writers[ns]; ok {
		return w
	}
	if r.homeResolver == nil {
		return nil
	}
	home, ok := r.homeResolver(ns)
	if !ok {
		return nil
	}
	cfg, _, _ := raglit.LoadConfig(home)
	if !cfg.QualityLog.On {
		return nil
	}
	w, err := raglit.NewQualityLogWriter(home, cfg.QualityLog.MaxBytes)
	if err != nil {
		return nil
	}
	r.writers[ns] = w
	return w
}

func (r *qualityLogRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ns, w := range r.writers {
		_ = w.Close()
		delete(r.writers, ns)
	}
}

// Daemon-side quality log recording: the two choke points every search and
// document fetch passes through (searchOp and getDocumentOp in httpd.go) log
// through the watcher, which owns the daemon's only project→home knowledge
// (the registration list) and so decides whether a project's log is on.

// namespaceOf picks the project namespace an event belongs to: the FIRST
// searched index's namespace (a search spanning projects is rare; the caller's
// own namespace is what "per-project" means). Empty scope → empty namespace →
// no log entry, which is correct for an unnamespaced single-home daemon.
func namespaceOf(names []string) string {
	for _, n := range names {
		if ns, _, ok := strings.Cut(n, nsSep); ok {
			return ns
		}
	}
	return ""
}

// recordSearch logs one search call with its hits, for the project whose
// indexes it searched. watch nil (tests, pre-daemon) or the project's log off
// → nothing is written, which is the default.
func recordSearch(w *watcher, ctx context.Context, ns, query, mode, scope, session string, hits []raglit.Hit, searched []string) {
	if w == nil {
		return
	}
	if ns == "" {
		ns = namespaceOf(searched)
	}
	log := w.QualityWriter(ns)
	if log == nil {
		return
	}
	qh := make([]raglit.QualityHit, 0, len(hits))
	for i, h := range hits {
		qh = append(qh, raglit.QualityHit{DocID: h.Path, Rank: i + 1, Score: h.Score, Origin: h.Origin, Chars: len(h.Text)})
	}
	_ = log.Record(raglit.QualityEvent{
		Type:    "search",
		Project: ns,
		Session: session,
		Query:   query,
		Mode:    mode,
		Index:   scope,
		Hits:    qh,
	})
}

// recordGetDocument logs one get_document fetch: the resolved document, the
// index it came from, and how much text came back.
func recordGetDocument(w *watcher, ctx context.Context, ns, path, docID, index, session string, chars int) {
	if w == nil {
		return
	}
	if ns == "" {
		ns = namespaceOf([]string{index})
	}
	log := w.QualityWriter(ns)
	if log == nil {
		return
	}
	_ = log.Record(raglit.QualityEvent{
		Type:      "get_document",
		Project:   ns,
		Session:   session,
		Path:      path,
		DocID:     docID,
		IndexHit:  index,
		Chars:     chars,
		FetchedBy: session,
	})
}
