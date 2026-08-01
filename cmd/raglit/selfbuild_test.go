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
