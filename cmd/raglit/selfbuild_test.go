package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The staleness check has to see a Go edit and ignore everything that cannot
// change what compiles — otherwise it either misses rebuilds or rebuilds on
// every log line written into the tree.
func TestSourceNewerThanSeesBuildInputsOnly(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	write := func(rel string, mod time.Time) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("old.go", base.Add(-time.Hour))
	if sourceNewerThan(dir, base) {
		t.Fatal("an untouched tree reported as stale — every run would rebuild")
	}
	write("notes.md", base.Add(time.Hour))
	if sourceNewerThan(dir, base) {
		t.Fatal("a markdown edit triggered a rebuild; it cannot change the binary")
	}
	write("sql/schema.sql", base.Add(time.Hour))
	if !sourceNewerThan(dir, base) {
		t.Fatal("an embedded schema change was missed — it IS compiled in")
	}
}

// .git churns constantly (index, ORIG_HEAD, logs). Walking it would report
// staleness after every git command and rebuild on a loop.
func TestSourceNewerThanPrunesGit(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	git := filepath.Join(dir, ".git")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .go file inside .git — hooks and sample files are real.
	p := filepath.Join(git, "hook.go")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := base.Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if sourceNewerThan(dir, base) {
		t.Fatal(".git was walked — every git command would trigger a rebuild")
	}
}

// A released build (no source stamp) must never rebuild anything, and neither
// must a spawned child or an explicit opt-out.
func TestSelfUpdateGuards(t *testing.T) {
	old := srcDir
	t.Cleanup(func() { srcDir = old })

	srcDir = ""
	if selfUpdateEnabled() {
		t.Error("a release build (no source stamp) would try to self-update")
	}
	srcDir = "/somewhere"
	for _, env := range []string{"RAGLIT_CHILD", "RAGLIT_AUTOBUILD_DONE"} {
		t.Setenv(env, "1")
		if selfUpdateEnabled() {
			t.Errorf("%s did not stop the self-update — a child rebuilding its parent's binary, or a re-exec loop", env)
		}
		t.Setenv(env, "")
	}
	t.Setenv("RAGLIT_NO_AUTOBUILD", "1")
	if selfUpdateEnabled() {
		t.Error("RAGLIT_NO_AUTOBUILD=1 did not opt out")
	}
}

// The daemon's guard has to be laxer than the CLI's, or it tracks its source
// once and never again: an auto-started daemon inherits RAGLIT_CHILD from the
// CLI that spawned it, and a daemon that re-execs would set AUTOBUILD_DONE.
func TestDaemonSelfUpdateOutlivesTheOneShotGuards(t *testing.T) {
	old := srcDir
	t.Cleanup(func() { srcDir = old })
	srcDir = "/somewhere"

	for _, env := range []string{"RAGLIT_CHILD", "RAGLIT_AUTOBUILD_DONE"} {
		t.Setenv(env, "1")
		if selfUpdateEnabled() {
			t.Errorf("%s must still stop the one-shot CLI path", env)
		}
		if !daemonSelfUpdateEnabled() {
			t.Errorf("%s disabled the daemon's watch — it would track source once, or never", env)
		}
		t.Setenv(env, "")
	}
	t.Setenv("RAGLIT_NO_AUTOBUILD", "1")
	if daemonSelfUpdateEnabled() {
		t.Error("RAGLIT_NO_AUTOBUILD=1 did not opt the daemon out")
	}
	t.Setenv("RAGLIT_NO_AUTOBUILD", "")
	srcDir = ""
	if daemonSelfUpdateEnabled() {
		t.Error("a release build (no source stamp) would watch for source it does not have")
	}
}

// Staleness has to be measured against the RUNNING image, not the file's mtime.
// `make install` rewrites the file, leaving it newer than the source and
// identical to nothing the daemon is executing — measured by mtime alone it
// reads "up to date" while serving an image two commits behind, which is what
// it did.
func TestHashFileSeesAReplacedBinary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "raglit")
	if err := os.WriteFile(p, []byte("build one"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := hashFile(p)
	if first == "" {
		t.Fatal("hashFile could not read a file it just wrote")
	}
	// Rewritten with a NEWER mtime, as an install would leave it.
	if err := os.WriteFile(p, []byte("build two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hashFile(p) == first {
		t.Fatal("a replaced binary hashed the same — the daemon would never adopt it")
	}
	if hashFile(filepath.Join(t.TempDir(), "absent")) != "" {
		t.Error("a missing file returned a hash")
	}
}
