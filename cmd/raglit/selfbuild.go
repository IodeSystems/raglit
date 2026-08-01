package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/iodesystems/raglit"
)

// Dev self-update — a source-stamped raglit rebuilds itself when the tree
// changed, then re-execs the fresh binary.
//
// Ported from dun, for the same reason and with the same guards: edit source,
// run the command again, and you transparently get the new build. What it
// prevents is not hypothetical here. A `raglit` twenty commits behind sat on
// PATH shadowing a current one, and answered `withdraw` with "unknown command"
// — a feature that existed, in a binary nobody had replaced.
//
// Guards:
//
//   - srcDir is stamped ONLY by `make build|install`. A plain
//     `go install ./cmd/raglit` leaves it empty → self-update is a no-op, which
//     is what a release build should do.
//   - RAGLIT_CHILD is set on every spawned subprocess (the auto-started daemon),
//     so a child never rebuilds a binary its parent just built.
//   - RAGLIT_AUTOBUILD_DONE guards the one re-exec after a rebuild (no loop).
//   - RAGLIT_NO_AUTOBUILD=1 disables it entirely.
//   - A build failure — a dirty tree mid-edit is the normal case — is not fatal:
//     warn and run the binary that exists.
//
// One difference from dun, and it is the whole reason this file is not a copy:
// raglit's long-lived process owns a JOB QUEUE. Re-execing it mid-ingest aborts
// the running job, which reclaimOrphanedJobs then marks errored and deliberately
// does NOT requeue. So the daemon rebuilds on its own schedule and re-execs only
// when the queue is idle — see daemonSelfUpdate.

// srcDir is the module directory, stamped at build time (see Makefile). Empty
// for released/plain builds → self-update disabled.
var srcDir = ""

// selfUpdate rebuilds and re-execs if the source tree is newer than this binary.
// Called once at the top of main, before any command runs.
func selfUpdate() {
	if !selfUpdateEnabled() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	st, err := os.Stat(exe)
	if err != nil || !sourceNewerThan(srcDir, st.ModTime()) {
		return // up to date, or cannot tell — proceed normally
	}
	fmt.Fprintln(os.Stderr, "raglit: source changed — rebuilding…")
	if err := rebuildRaglit(srcDir, exe); err != nil {
		fmt.Fprintf(os.Stderr, "raglit: rebuild failed (%v) — running the current binary\n", err)
		return
	}
	env := append(os.Environ(), "RAGLIT_AUTOBUILD_DONE=1")
	if err := syscall.Exec(exe, os.Args, env); err != nil {
		fmt.Fprintf(os.Stderr, "raglit: re-exec failed (%v) — running the current binary\n", err)
	}
}

func selfUpdateEnabled() bool {
	return srcDir != "" &&
		os.Getenv("RAGLIT_CHILD") == "" &&
		os.Getenv("RAGLIT_AUTOBUILD_DONE") == "" &&
		os.Getenv("RAGLIT_NO_AUTOBUILD") != "1"
}

// rebuildRaglit rebuilds the binary at exe from srcDir, source-stamped so the
// result self-updates too.
func rebuildRaglit(srcDir, exe string) error {
	// A temp output, moved into place: `go build -o exe` truncates the target
	// first, so a failed build would leave a zero-length binary where a working
	// one was — and this binary is the one running the rebuild.
	tmp := exe + ".new"
	build := exec.Command("go", "build",
		"-o", tmp,
		"-ldflags", "-X main.srcDir="+srcDir,
		"./cmd/raglit")
	build.Dir = srcDir
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Rename over the running binary. Legal on Linux: the running process keeps
	// its inode, and the next exec picks up the new one.
	return os.Rename(tmp, exe)
}

// sourceNewerThan reports whether any file that affects the build is newer than
// t. .git and testdata are pruned — neither changes what compiles.
func sourceNewerThan(dir string, t time.Time) bool {
	newer := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || newer {
			return err //nolint:nilerr // an unreadable corner must not claim staleness
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !buildInput(d.Name()) {
			return nil
		}
		if info, e := d.Info(); e == nil && info.ModTime().After(t) {
			newer = true
		}
		return nil
	})
	return newer
}

// buildInput reports whether a filename affects the compiled binary: Go source,
// the module files, the embedded schema and the embedded UI.
func buildInput(name string) bool {
	switch name {
	case "go.mod", "go.sum":
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".sql", ".html", ".css", ".js":
		return true
	}
	return false
}

// daemonSelfUpdate keeps the long-lived daemon on the current source, and is
// where raglit departs from dun.
//
// dun's launcher rebuilds and pushes a "reload" that its sessions act on when
// they choose. raglit's daemon has no such conversation with its work: it owns a
// queue, and re-execing mid-ingest aborts the running job. reclaimOrphanedJobs
// then marks it errored and deliberately does NOT requeue it — a job may have
// been killed BY its document, so retrying on every start is a crash loop. So an
// eager re-exec would trade "the daemon is stale" for "the document silently
// failed", which is the worse of the two.
//
// It therefore rebuilds as soon as the source changes — the build is the slow
// part and it costs the queue nothing — and re-execs only once the queue is
// idle. In practice that is seconds after a batch finishes, and never in the
// middle of one.
func daemonSelfUpdate(idle func() bool) {
	if !daemonSelfUpdateEnabled() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// The hash of the image THIS PROCESS is executing, taken before anything can
	// replace the file. Comparing source against the file's mtime is not enough:
	// `make install` and any CLI self-update rewrite the file, leaving it newer
	// than the source and identical to nothing this process is running. Measured
	// against mtime alone the daemon reads "up to date" while serving an image
	// two commits behind — which is exactly what it did.
	running := hashFile(exe)
	pending := false
	for range time.Tick(daemonWatchInterval) {
		if !pending {
			if st, err := os.Stat(exe); err == nil && sourceNewerThan(srcDir, st.ModTime()) {
				fmt.Fprintln(os.Stderr, "raglit daemon: source changed — rebuilding…")
				if err := rebuildRaglit(srcDir, exe); err != nil {
					fmt.Fprintf(os.Stderr, "raglit daemon: rebuild failed (%v) — staying on the current build\n", err)
					// Not retried on a timer: a tree that does not compile is the
					// normal state mid-edit, and a warning every few seconds is one
					// nobody reads. The next source change tries again.
					continue
				}
			}
			// Adopt whatever is on disk, whoever built it — this daemon's own
			// rebuild, a `make install`, or a CLI that self-updated. The question
			// is only ever "is the file different from what I am running", and it
			// has one answer for all three.
			if h := hashFile(exe); h != "" && running != "" && h != running {
				pending = true
				fmt.Fprintln(os.Stderr, "raglit daemon: a newer binary is on disk — restarting when the queue is idle")
			}
		}
		if !pending || !idle() {
			continue
		}
		fmt.Fprintln(os.Stderr, "raglit daemon: queue idle — restarting into the new build")
		// Deliberately NOT setting RAGLIT_AUTOBUILD_DONE. That guard bounds the
		// CLI to one re-exec per invocation; on a process that lives for days it
		// would mean the daemon tracks its source exactly once and then never
		// again. Nothing loops here without it: after the exec the running image
		// and the file agree, and the file is newer than the source.
		if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "raglit daemon: re-exec failed (%v) — staying up\n", err)
			pending = false
		}
	}
}

// daemonSelfUpdateEnabled is deliberately laxer than the CLI's guard.
//
// RAGLIT_CHILD and RAGLIT_AUTOBUILD_DONE exist to stop a one-shot command
// rebuilding twice or rebuilding what its parent just built. Neither applies to
// a process that lives for days: an auto-started daemon inherits RAGLIT_CHILD
// from the CLI that spawned it, and a daemon that re-execs sets AUTOBUILD_DONE —
// so honouring them would mean the daemon tracks its source once, or never.
func daemonSelfUpdateEnabled() bool {
	return srcDir != "" && os.Getenv("RAGLIT_NO_AUTOBUILD") != "1"
}

// hashFile is the sha256 of a file, or "" when it cannot be read. Uncached, on
// purpose: the whole question is whether the bytes on disk changed.
func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// daemonWatchInterval is how often the daemon looks for source changes. dun uses
// two seconds for a TUI a person is watching; this walks a source tree on a box
// also running ingest, and nobody is waiting on the poll.
const daemonWatchInterval = 5 * time.Second

// queuesIdle reports whether no index has work in flight or waiting.
//
// Every index, not just one: the daemon serves them all from one process, and
// restarting because THIS project is quiet would abort another project's ingest.
// An index that cannot be opened counts as busy — a restart on the strength of a
// state that could not be read is the wrong way to be wrong.
func queuesIdle(reg *raglit.Registry) bool {
	for _, name := range reg.Names() {
		st, err := reg.Get(name)
		if err != nil {
			return false
		}
		s, err := st.IndexStatus()
		if err != nil || s.Running > 0 || s.Pending > 0 {
			return false
		}
	}
	return true
}
