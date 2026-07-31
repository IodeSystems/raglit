package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit"
)

// A ruled copy must be announced, and a version must be announced DIFFERENTLY —
// re-reading a version to match its twin is exactly the wrong move, because it
// is a different filing that happens to share most of its text.
func TestRulingsDistinguishACopyFromAVersion(t *testing.T) {
	dir := t.TempDir()
	rel, err := raglit.LoadRelations(filepath.Join(dir, "relations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rel.Add(raglit.Mark{A: "scan.pdf", B: "original.pdf", Kind: raglit.MarkCopy}); err != nil {
		t.Fatal(err)
	}
	if err := rel.Add(raglit.Mark{A: "scan.pdf", B: "refiled.pdf", Kind: raglit.MarkVersion, Supersedes: "refiled.pdf"}); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, m := range rel.For("scan.pdf") {
		o, _ := m.Other("scan.pdf")
		got[o] = true
		if o == "original.pdf" && m.Kind != raglit.MarkCopy {
			t.Errorf("original.pdf should be a copy, got %q", m.Kind)
		}
		if o == "refiled.pdf" {
			if m.Kind != raglit.MarkVersion {
				t.Errorf("refiled.pdf should be a version, got %q", m.Kind)
			}
			if m.Supersedes != "refiled.pdf" {
				t.Errorf("lost which side governs: %+v", m)
			}
		}
	}
	for _, want := range []string{"original.pdf", "refiled.pdf"} {
		if !got[want] {
			t.Errorf("%s was not reported as related to the purged document", want)
		}
	}
}

// Paths in this corpus carry spaces and colons; an unquoted suggestion is a
// command that fails when pasted.
func TestTheSuggestedCommandIsRunnable(t *testing.T) {
	got := quoteArgs([]string{"a b/Fw: Ardley v. Brannock.pdf", "plain.pdf"})
	want := `"a b/Fw: Ardley v. Brannock.pdf" "plain.pdf"`
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

// Nothing to say must stay silent — a purge that reports on every document
// would bury the case where copies really do exist.
func TestSilentWhenThereAreNoOtherCopies(t *testing.T) {
	dir := t.TempDir()
	rel, _ := raglit.LoadRelations(filepath.Join(dir, "relations.jsonl"))
	if n := len(rel.For("lonely.pdf")); n != 0 {
		t.Errorf("invented %d relations", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "relations.jsonl")); !os.IsNotExist(err) {
		t.Error("loading a missing ruling file should not create it")
	}
}
