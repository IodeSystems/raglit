package main

import (
	"runtime/debug"

	"github.com/iodesystems/poly-lsp-mcp/segment"
	"github.com/iodesystems/raglit"
)

// polySegmenter is raglit.CodeSegmenter over poly-lsp-mcp's segment package:
// tree-sitter (gotreesitter, pure Go) with its grammars. This file is the only
// place raglit imports poly-lsp-mcp; the root package defines the interface
// and stays free of the parser (deps_test.go).
//
// Set as Models.Code only when config code_segmenter or --code-segmenter is
// on (llmFlags.models), so an index that never asked keeps its windows and
// its recipe.
type polySegmenter struct{}

var _ raglit.CodeSegmenter = polySegmenter{}

func (polySegmenter) ForPath(path string) (string, bool) { return segment.ForPath(path) }

func (polySegmenter) Symbols(lang string, content []byte) ([]raglit.CodeSpan, error) {
	sp, err := segment.Symbols(lang, content)
	return codeSpans(sp), err
}

func (polySegmenter) Sections(content []byte) ([]raglit.CodeSpan, error) {
	sp, err := segment.Sections(content)
	return codeSpans(sp), err
}

// Runtime names both the walk and the parser under it: a new poly-lsp-mcp can
// cut the same file differently on the same gotreesitter, and the reverse.
func (polySegmenter) Runtime() string {
	return "poly-lsp-mcp/segment@" + segmentVersion() + " " + segment.Runtime()
}

// segmentVersion is poly-lsp-mcp's module version in this binary's build
// info, "devel" when the binary has none (a go.work replace, go run).
func segmentVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/iodesystems/poly-lsp-mcp" {
				if d.Replace != nil && d.Replace.Version != "" {
					return d.Replace.Version
				}
				if d.Version != "" && d.Version != "(devel)" {
					return d.Version
				}
			}
		}
	}
	return "devel"
}

// codeSpans copies segment.Spans field for field; the two types are the same
// shape on purpose (raglit.CodeSpan).
func codeSpans(sp []segment.Span) []raglit.CodeSpan {
	if sp == nil {
		return nil
	}
	out := make([]raglit.CodeSpan, len(sp))
	for i, s := range sp {
		out[i] = raglit.CodeSpan{
			Sym: s.Sym, Class: s.Class,
			StartLine: s.StartLine, EndLine: s.EndLine,
			CommentStartLine: s.CommentStartLine, BodyStartLine: s.BodyStartLine,
		}
	}
	return out
}
