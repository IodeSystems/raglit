package raglit

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Tree watching (R10b, R12e): report the changes under a root as bursts. The
// watcher only reports; indexing them is a separate call (IndexTree with
// Changes.Paths).

// WatchOptions tune a watcher's debounce. A zero field takes its default;
// a negative MinInterval means none.
type WatchOptions struct {
	// Quiet: a burst ends after this long with no event. Default 500 ms.
	Quiet time.Duration
	// MaxDelay: a burst ends this long after its first event, even if events
	// keep coming. Default 5 s.
	MaxDelay time.Duration
	// MinInterval: at most one burst per this long. Default 2 s.
	MinInterval time.Duration
	// Idle: the watcher stops itself after this long with no Touch and no
	// event. 0 = never.
	Idle time.Duration
}

func (o WatchOptions) withDefaults() WatchOptions {
	if o.Quiet <= 0 {
		o.Quiet = 500 * time.Millisecond
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 5 * time.Second
	}
	if o.MinInterval == 0 {
		o.MinInterval = 2 * time.Second
	}
	if o.MinInterval < 0 {
		o.MinInterval = 0
	}
	return o
}

// Changes is one burst. Paths are absolute, sorted, and may name directories
// and paths that no longer exist; IndexTree's Paths takes them as they are.
// Empty Paths means the whole tree (Rescan, an event overflow). Degraded
// means the watcher hit the OS watch limit and stopped adding watches: from
// then on changes in new directories are not seen, and the caller rescans or
// polls on its own trigger.
type Changes struct {
	Root     string
	Paths    []string
	Degraded bool
}

// TreeWatcher watches one root. Make one with WatchTree.
type TreeWatcher struct {
	root  string
	rules TreeRules
	opts  WatchOptions
	fn    func(Changes)
	fw    *fsnotify.Watcher
	git   bool

	mu       sync.Mutex
	degraded bool
	ignored  map[string]bool // gitignored directories (rel), never watched

	rescanC chan struct{}
	touchC  chan struct{}
	stopC   chan struct{}
	done    chan struct{}
	once    sync.Once
	onStop  func() // WatchHub's hook, called once when the loop ends
}

// watchAdd adds one directory watch. A test replaces it to simulate the OS
// watch limit.
var watchAdd = func(fw *fsnotify.Watcher, dir string) error { return fw.Add(dir) }

// WatchTree watches the directories under root that r does not exclude
// (never .git, node_modules or a gitignored directory), adding directories as
// they appear, and calls fn with one Changes per burst. fn runs on the
// watcher's goroutine, one call at a time. The watcher stops on Stop, on
// ctx's end, or after o.Idle.
func WatchTree(ctx context.Context, root string, r TreeRules, o WatchOptions, fn func(Changes)) (*TreeWatcher, error) {
	return watchTree(ctx, root, r, o, fn, nil)
}

func watchTree(ctx context.Context, root string, r TreeRules, o WatchOptions, fn func(Changes), onStop func()) (*TreeWatcher, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, errors.New("raglit: watch tree: " + root + " is not a directory")
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &TreeWatcher{
		root: root, rules: r, opts: o.withDefaults(), fn: fn, fw: fw,
		ignored: map[string]bool{},
		rescanC: make(chan struct{}, 1), touchC: make(chan struct{}, 1),
		stopC: make(chan struct{}), done: make(chan struct{}),
		onStop: onStop,
	}
	if r.gitignore() && isGitWorkTree(root) {
		w.git = true
		w.ignored = gitIgnoredDirs(root)
	}
	w.addTree(root)
	go w.loop(ctx)
	return w, nil
}

// Rescan reports everything selected, as one burst (Changes with no Paths),
// as soon as MinInterval allows.
func (w *TreeWatcher) Rescan() {
	select {
	case w.rescanC <- struct{}{}:
	default:
	}
}

// Touch resets the idle timer.
func (w *TreeWatcher) Touch() {
	select {
	case w.touchC <- struct{}{}:
	default:
	}
}

// Stop ends the watcher. It only signals and never waits, so it is safe from
// inside fn and from a goroutine fn waits on: the watcher ends once a
// callback in progress returns, and calls fn no more. Wait waits for that.
func (w *TreeWatcher) Stop() {
	w.once.Do(func() { close(w.stopC) })
}

// Wait blocks until the watcher has ended (Stop, ctx's end, or Idle) and its
// callback in progress, if any, has returned. Wait must not be called from
// inside fn, or from a goroutine fn waits on: it would wait for itself.
func (w *TreeWatcher) Wait() { <-w.done }

// Root is the watched directory, absolute and clean.
func (w *TreeWatcher) Root() string { return w.root }

func (w *TreeWatcher) rel(p string) (string, bool) {
	if p == w.root {
		return ".", true
	}
	if !under(p, w.root) {
		return "", false
	}
	return filepath.ToSlash(strings.TrimPrefix(p, w.root+string(filepath.Separator))), true
}

// dirExcluded: the rules drop the directory and so everything in it. The
// probe child catches directory globs such as **/node_modules/**.
func (w *TreeWatcher) dirExcluded(abs, rel string) bool {
	if rel == "." {
		return false
	}
	probe := rel + "/_"
	if matchAny(builtinIgnore, rel) || matchAny(builtinIgnore, probe) ||
		matchAny(w.rules.Ignore, rel) || matchAny(w.rules.Ignore, probe) {
		return true
	}
	if w.rules.Skip != nil && w.rules.Skip(abs) {
		return true
	}
	w.mu.Lock()
	ign := w.ignored[rel]
	w.mu.Unlock()
	return ign
}

// addTree watches dir and the directories under it.
func (w *TreeWatcher) addTree(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, ok := w.rel(p)
		if !ok || w.dirExcluded(p, rel) {
			return filepath.SkipDir
		}
		w.mu.Lock()
		deg := w.degraded
		w.mu.Unlock()
		if deg {
			return filepath.SkipAll
		}
		if err := watchAdd(w.fw, p); err != nil {
			if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) {
				w.degrade(err)
				return filepath.SkipAll
			}
			return filepath.SkipDir // gone, or not readable
		}
		return nil
	})
}

func (w *TreeWatcher) degrade(err error) {
	w.mu.Lock()
	first := !w.degraded
	w.degraded = true
	w.mu.Unlock()
	if first {
		log.Printf("raglit: watch %s: %v — the OS watch limit is reached; no more directories are watched", w.root, err)
	}
}

func (w *TreeWatcher) isDegraded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.degraded
}

// event turns one fsnotify event into the paths it reports (none, or one).
func (w *TreeWatcher) event(ev fsnotify.Event) []string {
	p := filepath.Clean(ev.Name)
	rel, ok := w.rel(p)
	if !ok || rel == "." {
		return nil
	}
	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		// Gone: a file or a directory, no way to tell which. Include cannot
		// judge it; IndexTree finds what was under it.
		if w.rules.excluded(p, rel) || w.dirExcluded(p, rel) {
			return nil
		}
		return []string{p}
	}
	if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Write) {
		return nil // chmod
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return []string{p} // already gone again
	}
	if fi.IsDir() {
		if w.dirExcluded(p, rel) || w.git && gitIgnored(w.root, rel) {
			return nil
		}
		// Files may have landed before the watch did: report the directory.
		w.addTree(p)
		return []string{p}
	}
	if !w.rules.admits(p, rel) {
		return nil
	}
	return []string{p}
}

func (w *TreeWatcher) loop(ctx context.Context) {
	defer func() {
		_ = w.fw.Close()
		if w.onStop != nil {
			w.onStop()
		}
		close(w.done)
	}()
	o := w.opts
	pending := map[string]bool{}
	full, urgent := false, false
	reportDegraded := w.isDegraded()
	var first, last, lastEmit time.Time

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var idleC <-chan time.Time
	var idle *time.Timer
	if o.Idle > 0 {
		idle = time.NewTimer(o.Idle)
		defer idle.Stop()
		idleC = idle.C
	}
	resetIdle := func() {
		if idle != nil {
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(o.Idle)
		}
	}
	note := func(now time.Time) {
		if first.IsZero() {
			first = now
		}
		last = now
	}
	schedule := func() {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
		if len(pending) == 0 && !full && !reportDegraded {
			return
		}
		due := last.Add(o.Quiet)
		if c := first.Add(o.MaxDelay); c.Before(due) {
			due = c
		}
		if urgent || reportDegraded {
			due = time.Now()
		}
		if !lastEmit.IsZero() {
			if m := lastEmit.Add(o.MinInterval); due.Before(m) {
				due = m
			}
		}
		timer.Reset(max(time.Until(due), 0))
	}
	emit := func() {
		c := Changes{Root: w.root, Degraded: reportDegraded}
		if !full {
			for p := range pending {
				c.Paths = append(c.Paths, p)
			}
			if w.git {
				c.Paths = w.dropGitIgnored(c.Paths)
			}
			sort.Strings(c.Paths)
		}
		pending = map[string]bool{}
		hadFull := full
		full, urgent, reportDegraded = false, false, false
		first, last = time.Time{}, time.Time{}
		if len(c.Paths) == 0 && !hadFull && !c.Degraded {
			return
		}
		lastEmit = time.Now()
		w.fn(c)
	}
	schedule()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopC:
			return
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			wasDegraded := w.isDegraded()
			paths := w.event(ev)
			if !wasDegraded && w.isDegraded() {
				reportDegraded = true
			}
			if len(paths) > 0 {
				for _, p := range paths {
					pending[p] = true
				}
				note(time.Now())
				resetIdle()
			}
		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				full = true // events were lost: everything may have changed
				note(time.Now())
			} else {
				log.Printf("raglit: watch %s: %v", w.root, err)
			}
		case <-w.rescanC:
			full, urgent = true, true
			note(time.Now())
		case <-w.touchC:
			resetIdle()
			continue
		case <-timer.C:
			emit()
		case <-idleC:
			return
		}
		select {
		case <-w.stopC:
			return
		default:
		}
		schedule()
	}
}

// dropGitIgnored removes the paths git ignores (one git call per burst).
// Paths git cannot judge are kept.
func (w *TreeWatcher) dropGitIgnored(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	rels := make([]string, 0, len(paths))
	for _, p := range paths {
		r, _ := w.rel(p)
		rels = append(rels, r)
	}
	cmd := exec.Command("git", "-C", w.root, "check-ignore", "--stdin", "-z")
	cmd.Stdin = strings.NewReader(strings.Join(rels, "\x00") + "\x00")
	out, _ := cmd.Output() // exit 1 = nothing ignored
	ign := map[string]bool{}
	for _, r := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if r != "" {
			ign[r] = true
		}
	}
	kept := paths[:0]
	for i, p := range paths {
		if !ign[rels[i]] {
			kept = append(kept, p)
		}
	}
	return kept
}

// gitIgnoredDirs is the untracked directories git ignores under root, as
// slash paths relative to it.
func gitIgnoredDirs(root string) map[string]bool {
	out := map[string]bool{}
	b, err := exec.Command("git", "-C", root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z").Output()
	if err != nil {
		return out
	}
	for _, e := range strings.Split(string(b), "\x00") {
		if d, ok := strings.CutSuffix(e, "/"); ok && d != "" {
			out[d] = true
		}
	}
	return out
}

// gitIgnored reports whether git ignores rel under root.
func gitIgnored(root, rel string) bool {
	return exec.Command("git", "-C", root, "check-ignore", "-q", "--", rel).Run() == nil
}
