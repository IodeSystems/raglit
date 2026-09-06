package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A failed job whose file has since been renamed must be SKIPPED, not fatal.
// `ingest` aborts on the first missing path, so re-feeding a list of failures
// after a rename sweep killed the whole batch — which is the case `retry`
// exists to handle.
func TestLocalPathDistinguishesLocalFromRemote(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "here.pdf")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url, want string
	}{
		{"file://" + real, real},
		{real, real},
		{"https://example.com/a.pdf", ""},
		{"http://example.com/a.pdf", ""},
	} {
		if got := localPath(tc.url); got != tc.want {
			t.Errorf("localPath(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}

	// A remote URL has nothing to stat, so it must never be classed "gone" —
	// otherwise every http job would be silently dropped from a retry.
	if localPath("https://example.com/x.pdf") != "" {
		t.Error("remote URLs must not resolve to a local path")
	}

	// The gone-detection itself: a path that no longer exists.
	if _, err := os.Stat(localPath("file://" + filepath.Join(dir, "vanished.pdf"))); err == nil {
		t.Error("expected the missing file to fail stat")
	}
}
