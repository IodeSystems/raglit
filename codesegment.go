package raglit

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// CodeSpan is one cut of a source file, as a CodeSegmenter reports it: a
// declaration of a code file or a heading section of a Markdown file. Lines
// are 1-based and EndLine is exclusive. Sym is the dotted symbol path
// ("Server.Start") or, for a section, the heading path. Spans come in source
// order and are flat: nesting shows only in the ranges, a nested span lying
// inside its parent's.
//
// The same shape as poly-lsp-mcp's segment.Span, so an adapter is a field copy.
type CodeSpan struct {
	Sym, Class string
	StartLine  int
	EndLine    int
	// CommentStartLine is the first line of the doc comment above the
	// declaration, 0 when there is none.
	CommentStartLine int
	// BodyStartLine is the line where a callable's body begins, 0 when it has
	// none.
	BodyStartLine int
}

// CodeSegmenter cuts source text into CodeSpans. The root package defines the
// interface and does not implement it: raglitd sets Models.Code to an adapter
// over poly-lsp-mcp's segment package, so the parser and its grammars are not
// in the engine's import closure.
type CodeSegmenter interface {
	// ForPath names the language of a path, from its extension. ok is false
	// for a file the segmenter cannot parse.
	ForPath(path string) (lang string, ok bool)
	// Symbols returns the declarations of content, parsed as lang.
	Symbols(lang string, content []byte) ([]CodeSpan, error)
	// Sections returns one span per heading section of a Markdown document.
	Sections(content []byte) ([]CodeSpan, error)
	// Runtime names the parser and its version. It is in the pool recipe,
	// because two parsers can cut the same file differently.
	Runtime() string
}

// codeUnit is what the segmenter said about a text unit: its spans, and
// whether they are Markdown sections (cut at every heading) or code
// declarations (cut at top-level declarations).
type codeUnit struct {
	lang     string
	sections bool
	spans    []CodeSpan
}

// Fragment modes for a file cut by a CodeSegmenter.
const (
	fragModeCode     = "code"
	fragModeSections = "sections"
)

// segmentsPath reports whether a worker with segmenter cs reads path as
// KindCode: Markdown, or a file whose extension cs can parse.
func segmentsPath(cs CodeSegmenter, path string) bool {
	if cs == nil {
		return false
	}
	if isMarkdownPath(path) {
		return true
	}
	_, ok := cs.ForPath(path)
	return ok
}

// ingestCode indexes a code or Markdown file at its declarations or sections.
//
// When the segmenter fails, or finds nothing to cut at, the file is indexed
// by the text fragmenter exactly as it would be with no segmenter, and the
// stage log says why. A file the parser cannot read is still a file worth
// finding.
func (s *Store) ingestCode(ctx context.Context, cs CodeSegmenter, docPath, title string, data []byte, fc FragConfig, sl *StageLog) (int, string, error) {
	text := string(data)
	if cs == nil {
		return s.ingestText(ctx, docPath, title, text, true, fc, sl)
	}
	cu := &codeUnit{sections: isMarkdownPath(docPath)}
	var err error
	if cu.sections {
		cu.lang = "markdown"
		cu.spans, err = cs.Sections(data)
	} else {
		cu.lang, _ = cs.ForPath(docPath)
		cu.spans, err = cs.Symbols(cu.lang, data)
	}
	if err == nil && len(cu.spans) == 0 {
		err = errors.New("no spans found")
	}
	if err != nil {
		sl.Record("segment", "code:"+cu.lang, "degraded",
			fmt.Sprintf("fell back to %s: %v", fragModeOverlap, err))
		return s.ingestText(ctx, docPath, title, text, true, fc, sl)
	}
	u := ingestUnit{page: 0, text: text, fileText: true, code: cu}
	if cu.sections {
		u.frontmatter = frontmatterLen(text)
	}
	return s.ingestUnits(ctx, nil, nil, docPath, title, []ingestUnit{u}, fc, sl)
}

// codeNode is a span as a byte range of the file, with the spans inside it.
type codeNode struct {
	start, end int
	sym        string
	children   []*codeNode
}

// codeRange is one piece of the file before windowing.
type codeRange struct {
	start, end int
	sym        string
}

// fragmentCode cuts a file into fragments at its spans and sinks them in
// source order. The fragments tile the file, so get_document reassembles it
// exactly.
//
// Code: one fragment per top-level declaration, from its doc comment to its
// end. Text between declarations (package clause, imports, top-level
// statements) is its own fragment, with no symbol. Markdown: one fragment per
// heading section, its own text only (to the next heading), with the heading
// path as its symbol; text above the first heading has no symbol.
//
// A piece that oversize reports too big is cut at its nested spans (code),
// and what is still too big into the overlap windows. A piece with no letter
// or digit (blank lines, a closing brace) joins the piece before it, or the
// next one at the start of the file. The frontmatter block of a Markdown file
// is its own fragment, as fragmentOverlap makes it.
func fragmentCode(p resolvedPage, oversize func(string) bool, window, stride, floor int, sink func(stagedFrag)) int {
	src := p.text
	fm := p.frontmatter
	if fm < 0 || fm > len(src) {
		fm = 0
	}
	// starts[i] is the byte offset of line i+1.
	starts := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	offOf := func(line int) int {
		switch {
		case line < 1:
			return 0
		case line > len(starts):
			return len(src)
		}
		return starts[line-1]
	}
	// lineAt is the 1-based line holding byte off.
	lineAt := func(off int) int { return sort.Search(len(starts), func(i int) bool { return starts[i] > off }) }
	linesOf := func(start, end int) (int, int) { return lineAt(start), lineAt(end-1) + 1 }

	nodes := codeNodes(p.code, offOf, fm)

	// The pieces, in order, tiling [fm, len(src)).
	var pieces []codeRange
	add := func(start, end int, sym string) {
		if end > start {
			pieces = append(pieces, codeRange{start, end, sym})
		}
	}
	var walk func(n *codeNode)
	walk = func(n *codeNode) {
		if len(n.children) == 0 || !oversize(src[n.start:n.end]) {
			add(n.start, n.end, n.sym)
			return
		}
		at := n.start
		for _, c := range n.children {
			if c.start < at {
				continue // overlaps the child before it; its text stays in the parent's pieces
			}
			add(at, c.start, n.sym)
			walk(c)
			at = c.end
		}
		add(at, n.end, n.sym)
	}
	at := fm
	for _, n := range nodes {
		add(at, n.start, "")
		walk(n)
		at = n.end
	}
	add(at, len(src), "")

	// Fold pieces with no content into a neighbour.
	var merged []codeRange
	for _, r := range pieces {
		if contentChars(src[r.start:r.end]) == 0 && len(merged) > 0 {
			merged[len(merged)-1].end = r.end
			continue
		}
		if len(merged) == 1 && contentChars(src[merged[0].start:merged[0].end]) == 0 {
			r.start = merged[0].start
			merged[0] = r
			continue
		}
		merged = append(merged, r)
	}

	n := 0
	emit := func(start, end int, sym, origin string) {
		sl, el := linesOf(start, end)
		sink(stagedFrag{page: p.page, ord: n, startOff: start, endOff: end,
			startLine: sl, endLine: el, symbol: sym, text: src[start:end], origin: origin})
		n++
	}
	if fm > 0 {
		emit(0, fm, "", FragOriginFrontmatter)
	}
	for _, r := range merged {
		text := src[r.start:r.end]
		if !oversize(text) {
			emit(r.start, r.end, r.sym, "")
			continue
		}
		for _, of := range OverlapFragments(text, window, stride, floor) {
			emit(r.start+of.Start, r.start+of.End, r.sym, "")
		}
	}
	return n
}

// codeNodes turns spans into byte ranges past fm, sorted and nested, with no
// two siblings overlapping. A span that starts inside another but ends past
// it is clipped to start at that one's end, so the fragments never overlap.
//
// Code spans nest: a top-level node holds the spans inside it. Section spans
// are cut at every heading instead: each runs to the next heading, and none
// has children.
func codeNodes(cu *codeUnit, offOf func(int) int, fm int) []*codeNode {
	var flat []*codeNode
	maxEnd := 0
	for _, sp := range cu.spans {
		first := sp.StartLine
		if !cu.sections && sp.CommentStartLine > 0 && sp.CommentStartLine < first {
			first = sp.CommentStartLine
		}
		start, end := max(offOf(first), fm), offOf(sp.EndLine)
		if end <= start {
			continue
		}
		flat = append(flat, &codeNode{start: start, end: end, sym: sp.Sym})
		maxEnd = max(maxEnd, end)
	}
	// Outer before inner: by start, then the longer first.
	sort.SliceStable(flat, func(i, j int) bool {
		if flat[i].start != flat[j].start {
			return flat[i].start < flat[j].start
		}
		return flat[i].end > flat[j].end
	})
	if cu.sections {
		var out []*codeNode
		for i, n := range flat {
			if i+1 < len(flat) && flat[i+1].start == n.start {
				continue // the deeper section at this heading wins
			}
			end := maxEnd
			for _, m := range flat[i+1:] {
				if m.start > n.start {
					end = m.start
					break
				}
			}
			out = append(out, &codeNode{start: n.start, end: end, sym: n.sym})
		}
		return out
	}
	return nestNodes(flat)
}

// nestNodes builds the tree from sorted nodes: a node holds every following
// node that ends inside it.
func nestNodes(sorted []*codeNode) []*codeNode {
	var out []*codeNode
	for i := 0; i < len(sorted); {
		n := sorted[i]
		if len(out) > 0 {
			if prev := out[len(out)-1]; n.start < prev.end {
				// Starts inside the previous sibling and ends past it.
				n.start = prev.end
				if n.end <= n.start {
					i++
					continue
				}
			}
		}
		j := i + 1
		for j < len(sorted) && sorted[j].start < n.end && sorted[j].end <= n.end {
			j++
		}
		n.children = nestNodes(sorted[i+1 : j])
		out = append(out, n)
		i = j
	}
	return out
}
