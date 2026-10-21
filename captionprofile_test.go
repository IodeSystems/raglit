package raglit

import (
	"context"
	"strings"
	"testing"
)

func TestProfileFor(t *testing.T) {
	for _, c := range []struct {
		path string
		code bool
		want string
	}{
		{"file:///home/someone/proj/cmd/tool/main.go", true, "code"},
		{"file:///home/someone/proj/main.go", false, "code"}, // a source file is code anywhere
		{"file:///home/someone/proj/Makefile", false, "code"},
		{"file:///home/someone/proj/README.md", true, "docs"},
		{"file:///home/someone/case/timeline.md", false, "legal"},
		{"file:///home/someone/case/letter.txt", false, "legal"},
		{"file:///home/someone/case/saved-page.html", false, "legal"},
		{"file:///home/someone/proj/web/index.html", true, "code"},
		{"file:///home/someone/case/IMG_0412.jpg", false, "image"},
		{"file:///home/someone/proj/docs/shot.png", true, "image"},
		{"file:///home/someone/case/filing.pdf", true, "legal"},
	} {
		if got := profileFor(c.path, c.code).Family; got != c.want {
			t.Errorf("profileFor(%s, code=%v) = %s, want %s", c.path, c.code, got, c.want)
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
		IdentityAsk{Text: text, Path: path, Profile: profileFor(path, true)})
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
