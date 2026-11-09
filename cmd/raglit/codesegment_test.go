package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

const segGoFile = `package demo

import "fmt"

// Hello greets.
func Hello() {
	fmt.Println("hi")
}

type Server struct{}

// Start runs it.
func (s *Server) Start() error {
	return nil
}
`

const segMarkdown = `Intro line.

# Setup

Install it.

## Linux

apt install it.

# Usage

Run it.
`

// spanBy returns the span named sym, failing the test when there is none.
func spanBy(t *testing.T, sp []raglit.CodeSpan, sym string) raglit.CodeSpan {
	t.Helper()
	for _, s := range sp {
		if s.Sym == sym {
			return s
		}
	}
	t.Fatalf("no span %q in %+v", sym, sp)
	return raglit.CodeSpan{}
}

// saneLines fails on a span outside the file or empty: 1-based, EndLine
// exclusive, a comment not below the span's start. (segment's StartLine is
// the doc comment's line when there is one; the core cuts from the earlier of
// the two, so either reading gives the same fragment.)
func saneLines(t *testing.T, sp []raglit.CodeSpan, src string) {
	t.Helper()
	last := strings.Count(src, "\n") + 1
	for _, s := range sp {
		if s.StartLine < 1 || s.EndLine <= s.StartLine || s.EndLine > last {
			t.Errorf("span %q lines [%d,%d) outside 1..%d", s.Sym, s.StartLine, s.EndLine, last)
		}
		if s.CommentStartLine != 0 && s.CommentStartLine > s.StartLine {
			t.Errorf("span %q comment at %d, below its start %d", s.Sym, s.CommentStartLine, s.StartLine)
		}
	}
}

// The adapter carries segment's spans through unchanged on a real Go file:
// a span runs from its doc comment to the end of the declaration, the body
// line is the func line, and a method is under its receiver.
func TestPolySegmenter_GoSymbols(t *testing.T) {
	var cs polySegmenter
	lang, ok := cs.ForPath("pkg/demo.go")
	if !ok || lang != "go" {
		t.Fatalf("ForPath(demo.go) = %q, %v; want go, true", lang, ok)
	}
	if _, ok := cs.ForPath("notes.txt"); ok {
		t.Error("ForPath(notes.txt) is ok; want a file with no grammar refused")
	}
	sp, err := cs.Symbols(lang, []byte(segGoFile))
	if err != nil {
		t.Fatal(err)
	}
	saneLines(t, sp, segGoFile)
	if h := spanBy(t, sp, "Hello"); h.StartLine != 5 || h.EndLine != 9 || h.CommentStartLine != 5 || h.BodyStartLine != 6 {
		t.Errorf("Hello = %+v; want lines [5,9), comment at 5, body at 6", h)
	}
	if s := spanBy(t, sp, "Server.Start"); s.StartLine != 12 || s.EndLine != 16 || s.CommentStartLine != 12 || s.BodyStartLine != 13 {
		t.Errorf("Server.Start = %+v; want lines [12,16), comment at 12, body at 13", s)
	}
	spanBy(t, sp, "Server")
	if _, err := cs.Symbols("cobol", []byte("x")); err == nil {
		t.Error("Symbols(cobol) gave no error; want one for a language with no grammar")
	}
}

// Markdown: one span per heading section, its heading path as Sym.
func TestPolySegmenter_MarkdownSections(t *testing.T) {
	sp, err := polySegmenter{}.Sections([]byte(segMarkdown))
	if err != nil {
		t.Fatal(err)
	}
	saneLines(t, sp, segMarkdown)
	if len(sp) != 3 {
		t.Fatalf("%d section(s) %+v; want 3", len(sp), sp)
	}
	setup := sp[0]
	if setup.StartLine != 3 || setup.EndLine != 11 {
		t.Errorf("first section %+v; want Setup at lines [3,11)", setup)
	}
	if !strings.Contains(sp[1].Sym, "Linux") || sp[1].StartLine != 7 {
		t.Errorf("second section %+v; want Linux at line 7", sp[1])
	}
	if !strings.Contains(sp[2].Sym, "Usage") || sp[2].StartLine != 11 {
		t.Errorf("third section %+v; want Usage at line 11", sp[2])
	}
}

// Runtime names the walk's module version and the parser, so either changing
// re-keys the KindCode pool entries.
func TestPolySegmenter_Runtime(t *testing.T) {
	rt := polySegmenter{}.Runtime()
	if !strings.HasPrefix(rt, "poly-lsp-mcp/segment@") || !strings.Contains(rt, "gotreesitter") {
		t.Errorf("Runtime() = %q; want poly-lsp-mcp/segment@<ver> and gotreesitter", rt)
	}
}

// Off unless asked: no config key and no flag leaves Models.Code nil, so
// raglitd's fragments and pool keys are what they were.
func TestLLMFlags_CodeSegmenterSwitch(t *testing.T) {
	t.Setenv("RAGLIT_LLM_KEY", "")
	home := raglit.Home(t.TempDir())
	cfg := raglit.Config{VisionModel: "vis"}
	if err := raglit.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	if m := resolvedFlagsAt(t, home).models(home); m.Code != nil {
		t.Errorf("default Models.Code = %T; want nil", m.Code)
	}
	lf := resolvedFlagsAt(t, home)
	lf.codeSegmenter = true
	if _, ok := lf.models(home).Code.(polySegmenter); !ok {
		t.Error("--code-segmenter did not set Models.Code")
	}
	cfg.CodeSegmenter = true
	if err := raglit.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolvedFlagsAt(t, home).models(home).Code.(polySegmenter); !ok {
		t.Error("config code_segmenter did not set Models.Code")
	}
}

// With the segmenter on, a .go file is indexed one fragment per declaration,
// and a hit carries its lines and symbol.
func TestPolySegmenter_IngestGoFile(t *testing.T) {
	st, err := raglit.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := &raglit.Worker{Store: st, Code: polySegmenter{}, Fetcher: func(context.Context, string) (raglit.Fetched, error) {
		return raglit.Fetched{Data: []byte(segGoFile), Title: filepath.Base("demo.go")}, nil
	}}
	if _, err := st.Enqueue("demo.go", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		query, sym string
		start, end int
	}{
		{"greets", "Hello", 5, 10},
		{"runs", "Server.Start", 12, 16},
	} {
		hits, err := st.Search(c.query, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Fatalf("search %q: no hits", c.query)
		}
		h := hits[0]
		if h.Symbol != c.sym || h.StartLine != c.start || h.EndLine != c.end {
			t.Errorf("search %q: hit %s [%d,%d) %q; want %s [%d,%d)",
				c.query, h.Symbol, h.StartLine, h.EndLine, h.Text, c.sym, c.start, c.end)
		}
	}
}
