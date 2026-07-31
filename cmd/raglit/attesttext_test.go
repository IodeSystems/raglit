package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/attest"
)

// The offsets are the contract. A span that does not slice back to the text it
// claims is worse than no span at all: attest would show a reviewer one passage
// and record a verdict against another.
func TestTextParagraphOffsetsSliceBack(t *testing.T) {
	src := "First para line one.\nStill first.\n\n\nSecond para.\n\n  \n\tThird, indented.\n"
	spans := textParagraphs(src)
	want := []string{"First para line one.\nStill first.", "Second para.", "Third, indented."}
	if len(spans) != len(want) {
		t.Fatalf("got %d paragraphs, want %d: %#v", len(spans), len(want), spans)
	}
	for i, sp := range spans {
		if got := src[sp.From:sp.To]; got != want[i] {
			t.Errorf("span %d sliced %q, want %q", i, got, want[i])
		}
	}
}

// No span may open or close on whitespace — a reviewer cannot see it, and a
// digest taken over a trailing newline changes when an editor adds one.
func TestTextParagraphsAreTrimmed(t *testing.T) {
	for _, src := range []string{"\n\n  hello  \n\n", "one\n\n\n\ntwo", "\t\tonly\n"} {
		for _, sp := range textParagraphs(src) {
			got := src[sp.From:sp.To]
			if got != strings.TrimSpace(got) {
				t.Errorf("span over %q yielded untrimmed %q", src, got)
			}
		}
	}
}

func TestTextParagraphsEmpty(t *testing.T) {
	for _, src := range []string{"", "\n\n\n", "   \t  \n  \n"} {
		if got := textParagraphs(src); len(got) != 0 {
			t.Errorf("textParagraphs(%q) = %#v, want none", src, got)
		}
	}
}

// Evidence must digest the span alone while serving context, or editing a
// neighbouring paragraph would break the "this is the artifact" check on a
// passage nobody touched.
func TestTextEvidenceDigestsSpanNotContext(t *testing.T) {
	dir := t.TempDir()
	name := "note.md"
	body := "alpha paragraph.\n\nbeta paragraph.\n\ngamma paragraph.\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rd, err := readingFromText(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if len(rd.Units) != 3 {
		t.Fatalf("got %d units, want 3", len(rd.Units))
	}
	if rd.Asset.Kind != attest.KindText {
		t.Errorf("kind = %q, want %q", rd.Asset.Kind, attest.KindText)
	}
	mid := rd.Units[1]
	art, err := textEvidence{root: dir}.Render(context.Background(), rd.Asset, mid)
	if err != nil {
		t.Fatal(err)
	}
	if art.Digest != mid.Evidence {
		t.Errorf("served digest %s != recorded evidence %s", art.Digest, mid.Evidence)
	}
	if !strings.Contains(string(art.Body), "alpha") || !strings.Contains(string(art.Body), "gamma") {
		t.Errorf("context missing from rendered body: %q", art.Body)
	}
	if !strings.Contains(string(art.Body), ">>> beta paragraph.") {
		t.Errorf("the passage under review is not marked: %q", art.Body)
	}
}

// A file that changed under a recorded reading must say so rather than serve
// whatever now sits at those offsets.
func TestTextEvidenceRefusesShiftedOffsets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := attest.Unit{ID: "u1", Locator: attest.Locator{Span: &attest.Span{From: 0, To: 9999}}}
	_, err := textEvidence{root: dir}.Render(context.Background(), attest.Asset{ID: "a.md"}, u)
	if err == nil {
		t.Fatal("expected a refusal for offsets past the end of the file")
	}
	if !strings.Contains(err.Error(), "changed since it was read") {
		t.Errorf("error should name the cause, got: %v", err)
	}
}

func TestIsTextAssetExcludesPagedThings(t *testing.T) {
	yes := []string{"a.md", "b.txt", "c.eml", "D.MD"}
	no := []string{"a.pdf", "b.png", "c.pdf.raglit-transcription.md", "d.docx"}
	for _, p := range yes {
		if !isTextAsset(p) {
			t.Errorf("isTextAsset(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if isTextAsset(p) {
			t.Errorf("isTextAsset(%q) = true, want false", p)
		}
	}
}
