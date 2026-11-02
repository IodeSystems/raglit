package raglit

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestProfileFor(t *testing.T) {
	for _, c := range []struct {
		path   string
		corpus captionCorpus
		want   string
	}{
		{"file:///home/someone/proj/cmd/tool/main.go", corpusCode, "code"},
		{"file:///home/someone/proj/main.go", corpusDocs, "code"}, // a source file is code anywhere
		{"file:///home/someone/proj/Makefile", corpusRecords, "code"},
		{"file:///home/someone/proj/README.md", corpusCode, "docs"},
		{"file:///home/someone/proj/web/index.html", corpusCode, "code"},
		{"file:///home/someone/proj/docs/shot.png", corpusCode, "image"},
		{"file:///home/someone/proj/app.log", corpusCode, "docs"},
		{"file:///home/someone/proj/manual.pdf", corpusCode, "docs"},
		{"file:///home/someone/notes/idea.md", corpusDocs, "docs"},
		{"file:///home/someone/wiki/page.html", corpusDocs, "docs"},
		{"file:///home/someone/wiki/export.pdf", corpusDocs, "docs"},
		{"file:///home/someone/notes/untitled", corpusDocs, "docs"},
		{"file:///home/someone/case/timeline.md", corpusRecords, "legal"},
		{"file:///home/someone/case/letter.txt", corpusRecords, "legal"},
		{"file:///home/someone/case/saved-page.html", corpusRecords, "legal"},
		{"file:///home/someone/case/filing.pdf", corpusRecords, "legal"},
		{"file:///home/someone/case/IMG_0412.jpg", corpusRecords, "image"},
	} {
		if got := profileFor(c.path, c.corpus).Family; got != c.want {
			t.Errorf("profileFor(%s, %v) = %s, want %s", c.path, c.corpus, got, c.want)
		}
	}
}

// captionCorpusOf is an index holding one document per path.
func captionCorpusOf(t *testing.T, paths ...string) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, p := range paths {
		if _, err := s.db.Exec(`INSERT INTO documents(path, added_at) VALUES(?, 1)`, p); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func repeatPaths(n int, ext string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("file:///c/%d%s", i, ext)
	}
	return out
}

// A directory of notes is not a case folder: nothing about being "not a
// software project" makes it legal. A real case folder holds prose too, and
// stays legal because of the filings beside it.
func TestCaptionCorpus_ByShape(t *testing.T) {
	for _, c := range []struct {
		name  string
		paths []string
		want  captionCorpus
	}{
		{"empty", nil, corpusDocs},
		{"markdown only", repeatPaths(10, ".md"), corpusDocs},
		{"images and prose", append(repeatPaths(10, ".jpg"), repeatPaths(10, ".md")...), corpusDocs},
		{"source", append(repeatPaths(3, ".go"), repeatPaths(9, ".md")...), corpusCode},
		{"source beats records", append(repeatPaths(3, ".go"), repeatPaths(9, ".pdf")...), corpusCode},
		{"case folder", append(repeatPaths(6, ".pdf"), repeatPaths(4, ".txt")...), corpusRecords},
		{"mail and prose", append(repeatPaths(3, ".eml"), repeatPaths(9, ".md")...), corpusRecords},
		{"one pdf among notes", append(repeatPaths(1, ".pdf"), repeatPaths(9, ".md")...), corpusDocs},
	} {
		got, err := captionCorpusOf(t, c.paths...).captionCorpus()
		if err != nil || got != c.want {
			t.Errorf("%s: corpus = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestCaptionCorpus_OwnerChoiceBeatsShape(t *testing.T) {
	notes := captionCorpusOf(t, repeatPaths(10, ".md")...)
	if err := notes.SetCaptionProfile(CaptionProfileLegal, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := notes.captionCorpus(); got != corpusRecords {
		t.Errorf("legal chosen on notes: corpus = %v", got)
	}

	manuals := captionCorpusOf(t, repeatPaths(10, ".pdf")...)
	if err := manuals.SetCaptionProfile(CaptionProfileDocs, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := manuals.captionCorpus(); got != corpusDocs {
		t.Errorf("docs chosen on PDFs: corpus = %v", got)
	}
	if err := manuals.SetCaptionProfile("", 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := manuals.captionCorpus(); got != corpusRecords {
		t.Errorf("choice cleared: corpus = %v", got)
	}

	// docs replaces the records test only; a repository stays a repository.
	repo := captionCorpusOf(t, repeatPaths(10, ".go")...)
	if err := repo.SetCaptionProfile(CaptionProfileDocs, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.captionCorpus(); got != corpusCode {
		t.Errorf("docs chosen on source: corpus = %v", got)
	}

	if err := repo.SetCaptionProfile("code", 2); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

// The reported failure: a tool's document in a skills directory answered as what
// it is, and the legal enum refused it. It is now asked with docs kinds, and the
// prompt no longer offers deeds.
func TestIdentify_NoteOutsideAProjectGetsDocsRules(t *testing.T) {
	s := captionCorpusOf(t, "file:///skills/doctor/SKILL.md")
	ask, err := s.identityAsk("file:///skills/doctor/SKILL.md", strings.Repeat("How to run the doctor tool. ", 20))
	if err != nil {
		t.Fatal(err)
	}
	c := &identityChatter{replies: []string{`{"name":"Doctor tool — how to run it","kind":"guide",
	  "summary":"How to run the doctor tool and read its report, for someone diagnosing a local install.",
	  "content_tags":["doctor tool","install diagnosis","tool report"],"role_tags":["reference"]}`}}
	id, err := NewIdentifier(c, "m").Identify(context.Background(), ask)
	if err != nil {
		t.Fatal(err)
	}
	if id.Kind != "docs:guide" {
		t.Fatalf("kind = %q, want docs:guide", id.Kind)
	}
	for _, not := range []string{"legal", "deed", "instrument", "software project"} {
		if strings.Contains(c.prompts[0], not) {
			t.Errorf("prompt carries %q", not)
		}
	}
}

func TestProfileNormalize_StaysInItsFamily(t *testing.T) {
	for in, want := range map[string]string{
		"entrypoint": "code:entrypoint", "code:entrypoint": "code:entrypoint", "main": "code:entrypoint",
		"CLI": "code:entrypoint", "tests": "code:test", "Library": "code:library",
	} {
		if got, ok := codeProfile.normalize(in); !ok || got != want {
			t.Errorf("code normalize(%q) = %q,%v; want %q", in, got, ok, want)
		}
	}
	// A legal kind is no answer for a source file.
	for _, in := range []string{"deed", "legal:deed", "agreement", "docs:plan"} {
		if got, ok := codeProfile.normalize(in); ok {
			t.Errorf("code normalize(%q) = %q; a kind from another family was accepted", in, got)
		}
	}
}

// main.go is asked about as code: the prompt frames it as source, carries its
// path, lists code kinds and none of the legal ones; a "deed" answer is refused.
func TestIdentify_CodeFileGetsCodeRules(t *testing.T) {
	text := "package main\n\n" + strings.Repeat("// dispatches the tool's subcommands to their handlers\n", 6) +
		"func main() {\n\tos.Exit(run(os.Args[1:]))\n}\n"
	tail := `"summary":"The tool's program entry point: parses the subcommand and dispatches to its handler, exiting with its status.",
	  "content_tags":["command dispatch","cli entrypoint","exit status"],"role_tags":["implementation"]}`
	c := &identityChatter{replies: []string{
		`{"name":"Purchase agreement","kind":"deed",` + tail,
		`{"name":"tool CLI — subcommand dispatch","kind":"entrypoint",` + tail,
	}}
	path := "file:///home/someone/proj/cmd/tool/main.go"
	id, err := NewIdentifier(c, "m").Identify(context.Background(),
		IdentityAsk{Text: text, Path: path, Profile: profileFor(path, corpusCode)})
	if err != nil {
		t.Fatal(err)
	}
	if id.Kind != "code:entrypoint" || c.calls != 2 {
		t.Fatalf("kind = %q after %d calls, want code:entrypoint after 2", id.Kind, c.calls)
	}
	p := c.prompts[0]
	for _, want := range []string{"source code", "PURPOSE", "COMPONENT", "FILE PATH: proj/cmd/tool/main.go", "code:entrypoint"} {
		if !strings.Contains(p, want) {
			t.Errorf("code prompt lacks %q", want)
		}
	}
	for _, not := range []string{"legal", "deed", "instrument", "/home/someone"} {
		if strings.Contains(p, not) {
			t.Errorf("code prompt carries %q", not)
		}
	}
}

func TestCopiesNameExample_EveryProfile(t *testing.T) {
	if !copiesNameExample(codeProfile.NameExample, "package main") {
		t.Error("the code example name was not caught")
	}
	if copiesNameExample("Tidewater pump house", "the Tidewater pump house, north wall") {
		t.Error("a name the text itself carries was flagged")
	}
}

// A reasoning model thinks from the same token cap it answers from. Qwen3
// spent all 800 reasoning about a long plan and returned nothing, so the ask
// bounds the reasoning and leaves the answer its own cap on top.
func TestIdentify_ReasoningHasItsOwnBudget(t *testing.T) {
	c := &identityChatter{replies: []string{`{"name":"Purchase and sale agreement for the lot","summary":"The buyer's signed offer for the lot, between the two parties, with its closing date.","kind":"agreement","content_tags":["purchase agreement","property transfer","escrow closing"],"role_tags":["reference"]}`}}
	if _, err := NewIdentifier(c, "m").Identify(context.Background(), IdentityAsk{Text: psaText}); err != nil {
		t.Fatal(err)
	}
	o := c.opts[0]
	if o.ReasoningBudgetTokens == nil || *o.ReasoningBudgetTokens != identityReasoningBudget ||
		o.MaxTokens != identityMaxTokens+identityReasoningBudget {
		t.Fatalf("opts = %+v — the reasoning budget is not separate from the answer cap", o)
	}
}
