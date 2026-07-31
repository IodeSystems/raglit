package main

import (
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit"
)

// A ruled copy must be announced, and a version must be announced DIFFERENTLY —
// re-reading a version to match its twin is exactly the wrong move, because it
// is a different filing that happens to share most of its text.
func TestRulingsDistinguishACopyFromAVersion(t *testing.T) {
	dir := t.TempDir()
	js, err := raglit.OpenJudgements(filepath.Join(dir, "judgements.db"), filepath.Join(dir, "raglit-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	if err := js.PutRelation(raglit.Mark{A: "scan.pdf", B: "original.pdf", Kind: raglit.MarkCopy}); err != nil {
		t.Fatal(err)
	}
	if err := js.PutRelation(raglit.Mark{A: "scan.pdf", B: "refiled.pdf", Kind: raglit.MarkVersion, Supersedes: "refiled.pdf"}); err != nil {
		t.Fatal(err)
	}

	rels, err := js.RelationsFor("scan.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range rels {
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
	js, err := raglit.OpenJudgements(filepath.Join(dir, "judgements.db"), filepath.Join(dir, "raglit-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	rels, err := js.RelationsFor("lonely.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 0 {
		t.Errorf("invented %d relations", len(rels))
	}
}
