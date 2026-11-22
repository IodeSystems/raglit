package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// TestSweepIndexGitRefusal verifies that sweepIndex reports per-document
// refused-for-git-tree in Reasons when a document sits inside a git working
// tree, and writes sidecars normally for documents in a plain folder.
func TestSweepIndexGitRefusal(t *testing.T) {
	home := raglit.Home(t.TempDir())
	reg, err := raglit.OpenRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	st, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}

	// A fake git working tree: a directory that contains a .git entry.
	gitRepo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(gitRepo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(gitRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A plain folder with no .git anywhere above it.
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write text assets in both locations.
	gitDoc := filepath.Join(gitRepo, "src", "readme.md")
	if err := os.WriteFile(gitDoc, []byte("# hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plainDoc := filepath.Join(plain, "notes.md")
	if err := os.WriteFile(plainDoc, []byte("a plain note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Ingest both documents so the store knows about them.
	for _, d := range []raglit.Document{
		{Path: gitDoc, Title: "readme.md", Fragments: []raglit.Fragment{{Text: "hello world"}}},
		{Path: plainDoc, Title: "notes.md", Fragments: []raglit.Fragment{{Text: "a plain note"}}},
	} {
		if err := st.Ingest(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}

	// The index root must be a prefix of both document paths so filepath.Rel
	// works. The parent of both temp dirs is t.TempDir()'s parent, but since
	// they are separate temp dirs, use a common ancestor.
	// Actually, the two temp dirs are siblings under the OS temp directory.
	// Use the common parent.
	common := filepath.Dir(gitRepo) // the first TempDir
	_ = common

	// sweepIndex takes (st, root) where root is the index root for
	// computing relative paths in verdict projection. For this test the
	// relative-path detail doesn't matter; use "/" as a neutral root that
	// won't cause issues with filepath.Rel on absolute paths.
	res, err := sweepIndex(st, "/")
	if err != nil {
		t.Fatal(err)
	}

	// The git-repo document must be in Reasons with the git-refusal message.
	reason, ok := res.Reasons["readme.md"]
	if !ok {
		t.Fatalf("expected readme.md in Reasons, got %+v", res.Reasons)
	}
	if !strings.Contains(reason, "git working tree") {
		t.Errorf("reason for readme.md does not mention git working tree: %q", reason)
	}

	// The plain-folder document must have been written (in Wrote count, not in Reasons).
	if res.Wrote != 1 {
		t.Errorf("expected Wrote=1, got %d", res.Wrote)
	}
	if _, refused := res.Reasons["notes.md"]; refused {
		t.Errorf("notes.md should not be in Reasons, got %q", res.Reasons["notes.md"])
	}

	// Verify the sidecar file exists for the plain document.
	sidecar := plainDoc + ".reading.json"
	if _, err := os.Stat(sidecar); err != nil {
		t.Errorf("expected sidecar %s to exist: %v", sidecar, err)
	}

	// Verify NO sidecar was written for the git-repo document.
	gitSidecar := gitDoc + ".reading.json"
	if _, err := os.Stat(gitSidecar); !os.IsNotExist(err) {
		t.Errorf("expected no sidecar for git-repo doc, but %s exists", gitSidecar)
	}
}
