package raglit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `## Page N` headings are the contract. A consumer resolving a match to a
// page depends on them being complete and monotonic — dropping a blank page
// would silently shift every page number after it, which is exactly the class of
// error this whole feature exists to remove.
func TestTranscriptionKeepsEveryPageIncludingEmptyOnes(t *testing.T) {
	md := RenderTranscription("/x/deed.pdf", []TranscribedPage{
		{Page: 1, Text: "first page"},
		{Page: 2, Text: "   "}, // scanned blank
		{Page: 3, Text: "third page"},
	})
	for _, want := range []string{"## Page 1", "## Page 2", "## Page 3"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(md, "_(no text on this page)_") {
		t.Error("an empty page must be marked, not omitted")
	}
	if strings.Index(md, "## Page 1") > strings.Index(md, "## Page 2") {
		t.Error("pages must be in order")
	}
}

// Out-of-order input still renders in page order — a caller assembling pages
// concurrently must not be able to corrupt the numbering.
func TestTranscriptionSortsByPage(t *testing.T) {
	md := RenderTranscription("/x/d.pdf", []TranscribedPage{
		{Page: 3, Text: "c"}, {Page: 1, Text: "a"}, {Page: 2, Text: "b"},
	})
	if i, j := strings.Index(md, "## Page 1"), strings.Index(md, "## Page 3"); i > j {
		t.Error("pages rendered out of order")
	}
}

// A scanned survey is mostly figure. A transcription that drops the description
// reports the page as blank.
func TestTranscriptionIncludesFigureDescriptions(t *testing.T) {
	md := RenderTranscription("/x/survey.pdf", []TranscribedPage{{
		Page: 1, Text: "SURVEY",
		Figures: []TranscribedFigure{{Kind: "diagram", Description: "plat showing lots F, G, H"}},
	}})
	if !strings.Contains(md, "plat showing lots F, G, H") {
		t.Error("figure description dropped")
	}
	if !strings.Contains(md, "**diagram:**") {
		t.Error("figure should be labelled by kind")
	}
}

// Deterministic, so a diff between two transcriptions is a real change in what
// was read rather than formatting noise.
func TestTranscriptionIsDeterministic(t *testing.T) {
	pages := []TranscribedPage{{Page: 1, Text: "a"}, {Page: 2, Text: "b"}}
	if RenderTranscription("/x/d.pdf", pages) != RenderTranscription("/x/d.pdf", pages) {
		t.Error("render is not deterministic")
	}
}

func TestTranscriptionPathIsBesideTheDocument(t *testing.T) {
	if got := TranscriptionPath("/a/b/c.pdf"); got != "/a/b/c.pdf.raglit-transcription.md" {
		t.Errorf("got %q", got)
	}
}

// The option has to reach the Store, not merely exist in the config struct.
//
// Three earlier fixes in this area were written, tested at the unit level, and
// then did nothing because nothing consulted them on the real path. This test is
// specifically about the wiring.
func TestWritebackConfigReachesTheStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  string
		want bool
	}{
		{"off by default", `{"project":"p"}`, false},
		{"project-wide", `{"project":"p","writeback_transcription_md":true}`, true},
		{"per-index", `{"project":"p","indexes":{"default":{"writeback_transcription_md":true}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := OpenHome(Home(dir))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.writebackTranscription != tc.want {
				t.Errorf("writeback = %v, want %v — config did not reach the store",
					s.writebackTranscription, tc.want)
			}
		})
	}
}

// The project's own config must decide, because the daemon never reads it:
// home discovery picks the project home OR the default home and never overlays,
// so a per-project setting was otherwise only expressible in the daemon's global
// config under a namespaced index key.
func TestProjectConfigDecidesWritebackForItsOwnDocuments(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "matter")
	docs := filepath.Join(proj, "documents", "court")
	if err := os.MkdirAll(filepath.Join(proj, ProjectHomeName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(docs, "order.pdf")
	write := func(cfg string) {
		if err := os.WriteFile(filepath.Join(proj, ProjectHomeName, "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"project":"m","writeback_transcription_md":true}`)
	if !writebackForDoc(doc, false) {
		t.Error("a project that asks for transcriptions must get them even when the daemon default is off")
	}
	write(`{"project":"m","writeback_transcription_md":false}`)
	if writebackForDoc(doc, true) {
		t.Error("a project that declines must not get them even when the daemon default is on")
	}
	write(`{"project":"m"}`)
	for _, fb := range []bool{true, false} {
		if got := writebackForDoc(doc, fb); got != fb {
			t.Errorf("a project with no opinion must defer to the store (%v), got %v", fb, got)
		}
	}
	// No project config anywhere: the store's setting stands.
	outside := filepath.Join(root, "elsewhere", "x.pdf")
	if got := writebackForDoc(outside, true); !got {
		t.Error("with no project config the store setting must stand")
	}
}
