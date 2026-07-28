package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With no project to resolve, `raglit status` must SHOW what exists rather than
// only naming a config file to edit. The indexes the daemon holds, and where they
// live on disk, were otherwise undiscoverable from the command whose entire job
// is reporting state.
func TestStatusWithoutAProjectListsTheIndexes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RAGLIT_ROOT", root)
	for _, name := range []string{"ardley-v-brannock__default", "raglit__code"} {
		if err := os.MkdirAll(filepath.Join(root, "indexes", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdout(t, func() {
		if err := printIndexDirectory(nil); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"not in a project",
		"ardley-v-brannock__default",
		"raglit__code",
		filepath.Join(root, "indexes"), // the PATH, which is the undiscoverable part
		"--project",                    // and how to act on one
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// An empty root says so, and points at the command that creates one, instead of
// printing an empty table.
func TestStatusWithNoIndexesSaysSo(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RAGLIT_ROOT", root)
	out := captureStdout(t, func() {
		if err := printIndexDirectory(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no indexes") || !strings.Contains(out, "raglit init") {
		t.Errorf("an empty root should say so and name the fix:\n%s", out)
	}
}

// An unreadable index reports "?" rather than 0: zero is a claim about the
// index, "?" is a claim about our access to it.
func TestUnreadableIndexReportsUnknownNotZero(t *testing.T) {
	d, f := indexCounts(filepath.Join(t.TempDir(), "nope", "index.sqlite"))
	if d != "?" || f != "?" {
		t.Errorf("got %q/%q, want ?/? — 0 would assert the index is empty", d, f)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	return string(buf[:n])
}
