package raglit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeCode is a CodeSegmenter that returns fixed spans. .go is "go", .md is
// "markdown", and anything else it cannot parse.
type fakeCode struct {
	symbols  []CodeSpan
	sections []CodeSpan
	err      error
	runtime  string
}

func (f *fakeCode) ForPath(path string) (string, bool) {
	switch filepath.Ext(path) {
	case ".go":
		return "go", true
	case ".md":
		return "markdown", true
	}
	return "", false
}

func (f *fakeCode) Symbols(lang string, content []byte) ([]CodeSpan, error) {
	if lang != "go" {
		return nil, fmt.Errorf("no grammar for %q", lang)
	}
	return f.symbols, f.err
}

func (f *fakeCode) Sections(content []byte) ([]CodeSpan, error) { return f.sections, f.err }

func (f *fakeCode) Runtime() string {
	if f.runtime == "" {
		return "fake@1"
	}
	return f.runtime
}

type codeFrag struct {
	StartLine, EndLine int
	Symbol, Text       string
	Origin             string
}

func docFrags(t *testing.T, s *Store, path string) []codeFrag {
	t.Helper()
	rows, err := s.db.Query(`SELECT f.start_line, f.end_line, f.symbol, f.text, f.origin
		FROM fragments f JOIN documents d ON d.id=f.doc_id
		WHERE d.path=? AND f.origin <> 'identity' ORDER BY f.page, f.ord`, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []codeFrag
	for rows.Next() {
		var r codeFrag
		if err := rows.Scan(&r.StartLine, &r.EndLine, &r.Symbol, &r.Text, &r.Origin); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func stageDump(t *testing.T, s *Store, jobID int64) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name, engine, state, detail FROM job_stages WHERE job_id=? ORDER BY seq`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var n, e, st, d string
		if rows.Scan(&n, &e, &st, &d) == nil {
			fmt.Fprintf(&b, "%s %s %s %s\n", n, e, st, d)
		}
	}
	return b.String()
}

const goFile = `package demo

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

var goSpans = []CodeSpan{
	{Sym: "Hello", Class: "function", StartLine: 6, EndLine: 9, CommentStartLine: 5, BodyStartLine: 6},
	{Sym: "Server", Class: "struct", StartLine: 10, EndLine: 11},
	{Sym: "Server.Start", Class: "method", StartLine: 13, EndLine: 16, CommentStartLine: 12, BodyStartLine: 13},
}

// One fragment per top-level declaration, from its doc comment; the package
// clause and imports are their own fragment; blank lines join the fragment
// before them; the fragments tile the file.
func TestIngestCode_OneFragmentPerDeclaration(t *testing.T) {
	s := openMem(t)
	n, mode, err := s.ingestCode(context.Background(), &fakeCode{symbols: goSpans}, "demo.go", "demo",
		[]byte(goFile), FragConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mode != fragModeCode || n != 4 {
		t.Fatalf("mode %q, %d fragment(s); want code, 4", mode, n)
	}
	got := docFrags(t, s, "demo.go")
	want := []codeFrag{
		{1, 5, "", "package demo\n\nimport \"fmt\"\n\n", ""},
		{5, 10, "Hello", "// Hello greets.\nfunc Hello() {\n\tfmt.Println(\"hi\")\n}\n\n", ""},
		{10, 12, "Server", "type Server struct{}\n\n", ""},
		{12, 16, "Server.Start", "// Start runs it.\nfunc (s *Server) Start() error {\n\treturn nil\n}\n", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fragments:\n got %+v\nwant %+v", got, want)
	}
	doc, err := s.DocText("demo.go", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != goFile {
		t.Fatalf("reassembly not exact:\n got %q\nwant %q", doc.Text, goFile)
	}
}

// A declaration over the embed limit is cut at its nested declarations; one
// with none is cut into windows, each keeping the declaration's symbol.
func TestIngestCode_OversizeDeclarationSplits(t *testing.T) {
	body := func(name string) string {
		return "  " + name + "() {\n    return " + strings.Repeat(name, 60) + "\n  }\n"
	}
	var b strings.Builder
	b.WriteString("class Big {\n")       // 1
	b.WriteString("  // a does a.\n")    // 2
	b.WriteString(body("a"))             // 3-5
	b.WriteString(body("b"))             // 6-8
	b.WriteString("}\n")                 // 9
	b.WriteString("function long() {\n") // 10
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "  step%02d();\n", i) // 11-30
	}
	b.WriteString("}\n") // 31
	src := b.String()
	spans := []CodeSpan{
		{Sym: "Big", StartLine: 1, EndLine: 10},
		{Sym: "Big.a", StartLine: 3, EndLine: 6, CommentStartLine: 2},
		{Sym: "Big.b", StartLine: 6, EndLine: 9},
		{Sym: "long", StartLine: 10, EndLine: 32},
	}
	s := openMem(t)
	fc := FragConfig{EmbedLimit: 120}
	if _, _, err := s.ingestCode(context.Background(), &fakeCode{symbols: spans}, "big.go", "big",
		[]byte(src), fc, nil); err != nil {
		t.Fatal(err)
	}
	got := docFrags(t, s, "big.go")
	if len(got) < 5 {
		t.Fatalf("got %d fragment(s): %+v", len(got), got)
	}
	head := []codeFrag{
		{1, 2, "Big", "class Big {\n", ""},
		{2, 6, "Big.a", "  // a does a.\n" + body("a"), ""},
		{6, 10, "Big.b", body("b") + "}\n", ""},
	}
	if !reflect.DeepEqual(got[:3], head) {
		t.Fatalf("nested split:\n got %+v\nwant %+v", got[:3], head)
	}
	rest := got[3:]
	if len(rest) < 2 {
		t.Fatalf("long() was not windowed: %+v", rest)
	}
	for _, f := range rest {
		// At most a window plus the floor: OverlapFragments folds a short tail
		// into the last window.
		if f.Symbol != "long" || len(f.Text) > 240 {
			t.Errorf("window %+v: want symbol long and at most 240 bytes", f)
		}
		if f.StartLine < 10 || f.EndLine > 32 || f.EndLine <= f.StartLine {
			t.Errorf("window lines [%d,%d) outside long()'s [10,32)", f.StartLine, f.EndLine)
		}
		lines := strings.SplitAfter(src, "\n")
		if span := strings.Join(lines[f.StartLine-1:f.EndLine-1], ""); !strings.Contains(span, f.Text) {
			t.Errorf("window %q is not inside its lines [%d,%d)", f.Text, f.StartLine, f.EndLine)
		}
	}
	if rest[0].StartLine != 10 || rest[len(rest)-1].EndLine != 32 {
		t.Errorf("windows cover [%d,%d), want [10,32)", rest[0].StartLine, rest[len(rest)-1].EndLine)
	}
	doc, _ := s.DocText("big.go", 0, 0, 0)
	if doc.Text != src {
		t.Fatalf("reassembly not exact:\n got %q\nwant %q", doc.Text, src)
	}
}

// With no embed limit known, a declaration over the window is still cut: the
// window bounds it, as it bounds the text path.
func TestIngestCode_UnknownLimitBoundedByWindow(t *testing.T) {
	var b strings.Builder
	b.WriteString("var table = []string{\n") // 1
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "\t\"row %03d of a generated table\",\n", i) // 2-401
	}
	b.WriteString("}\n") // 402
	src := b.String()
	spans := []CodeSpan{{Sym: "table", StartLine: 1, EndLine: 403}}
	s := openMem(t)
	if _, _, err := s.ingestCode(context.Background(), &fakeCode{symbols: spans}, "table.go", "table",
		[]byte(src), FragConfig{}, nil); err != nil {
		t.Fatal(err)
	}
	window, _, floor := ResolveFragParams(0, 0, 0, 0)
	got := docFrags(t, s, "table.go")
	if len(got) < 2 {
		t.Fatalf("a %d-byte declaration with no known limit is %d fragment(s)", len(src), len(got))
	}
	for _, f := range got {
		if f.Symbol != "table" || len(f.Text) > window+floor {
			t.Errorf("fragment [%d,%d) symbol %q, %d bytes: want table and at most %d",
				f.StartLine, f.EndLine, f.Symbol, len(f.Text), window+floor)
		}
	}
	doc, _ := s.DocText("table.go", 0, 0, 0)
	if doc.Text != src {
		t.Fatal("reassembly not exact")
	}
}

// A parse error, or no spans, indexes the file exactly as the text fragmenter
// does, and the stage log names the error.
func TestIngestCode_ParseErrorFallsBack(t *testing.T) {
	text := strings.Repeat("func broken( {\n", 30)
	fc := FragConfig{Window: 120, Stride: 80, Floor: 30}
	ref := openMem(t)
	if _, _, err := ref.ingestText(context.Background(), "x.go", "x", text, true, fc, nil); err != nil {
		t.Fatal(err)
	}
	want := docFrags(t, ref, "x.go")

	for name, cs := range map[string]*fakeCode{
		"error":    {err: errors.New("syntax error at line 1")},
		"no spans": {},
	} {
		s := openMem(t)
		id, _ := s.Enqueue("x.go", "")
		_, mode, err := s.ingestCode(context.Background(), cs, "x.go", "x", []byte(text), fc, s.NewStageLog(id))
		if err != nil {
			t.Fatal(err)
		}
		if mode != fragModeOverlap {
			t.Errorf("%s: mode %q, want %s", name, mode, fragModeOverlap)
		}
		if got := docFrags(t, s, "x.go"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: fallback fragments differ from the text fragmenter's", name)
		}
		dump := stageDump(t, s, id)
		wantDetail := "syntax error at line 1"
		if cs.err == nil {
			wantDetail = "no spans found"
		}
		if !strings.Contains(dump, "segment code:go degraded") || !strings.Contains(dump, wantDetail) {
			t.Errorf("%s: stage log does not record the fallback:\n%s", name, dump)
		}
	}
}

// Markdown is cut at every heading: one fragment per section, its own text,
// with the heading path as the symbol. Frontmatter and text above the first
// heading are their own fragments.
func TestIngestCode_MarkdownSections(t *testing.T) {
	md := "---\ntitle: x\n---\nIntro text.\n# A\nalpha\n## B\nbeta\n# C\ngamma\n"
	sections := []CodeSpan{
		{Sym: "A", Class: "section", StartLine: 5, EndLine: 9},
		{Sym: "A/B", Class: "section", StartLine: 7, EndLine: 9},
		{Sym: "C", Class: "section", StartLine: 9, EndLine: 11},
	}
	s := openMem(t)
	_, mode, err := s.ingestCode(context.Background(), &fakeCode{sections: sections}, "doc.md", "doc",
		[]byte(md), FragConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mode != fragModeSections {
		t.Fatalf("mode %q, want %s", mode, fragModeSections)
	}
	want := []codeFrag{
		{1, 4, "", "---\ntitle: x\n---\n", FragOriginFrontmatter},
		{4, 5, "", "Intro text.\n", ""},
		{5, 7, "A", "# A\nalpha\n", ""},
		{7, 9, "A/B", "## B\nbeta\n", ""},
		{9, 11, "C", "# C\ngamma\n", ""},
	}
	if got := docFrags(t, s, "doc.md"); !reflect.DeepEqual(got, want) {
		t.Fatalf("sections:\n got %+v\nwant %+v", got, want)
	}

	// The same oversize rule: a section over the limit is windowed.
	long := "# Long\n" + strings.Repeat("words and more words\n", 20)
	s2 := openMem(t)
	if _, _, err := s2.ingestCode(context.Background(),
		&fakeCode{sections: []CodeSpan{{Sym: "Long", StartLine: 1, EndLine: 22}}}, "long.md", "long",
		[]byte(long), FragConfig{EmbedLimit: 100}, nil); err != nil {
		t.Fatal(err)
	}
	got := docFrags(t, s2, "long.md")
	if len(got) < 2 {
		t.Fatalf("oversize section not windowed: %+v", got)
	}
	for _, f := range got {
		if f.Symbol != "Long" || len(f.Text) > 200 {
			t.Errorf("window %+v: want symbol Long and at most 200 bytes", f)
		}
	}
}

// Through the worker: with no segmenter a .go file is read exactly as before;
// with one it is KindCode and cut at its declarations.
func TestWorker_CodeSegmenterRouting(t *testing.T) {
	ingest := func(cs CodeSegmenter, path string, data []byte) (*Store, string) {
		s := openMem(t)
		w := &Worker{Store: s, Code: cs, Fetcher: func(context.Context, string) (Fetched, error) {
			return Fetched{Data: data, Title: filepath.Base(path)}, nil
		}}
		if _, err := s.Enqueue(path, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ProcessOne(context.Background()); err != nil {
			t.Fatal(err)
		}
		var mode string
		s.db.QueryRow(`SELECT frag_mode FROM documents WHERE path=?`, path).Scan(&mode)
		return s, mode
	}

	// Reference: the text fragmenter as it ran before KindCode existed.
	ref := openMem(t)
	if _, _, err := ref.ingestText(context.Background(), "demo.go", "demo.go", goFile, true, FragConfig{}, nil); err != nil {
		t.Fatal(err)
	}
	s, mode := ingest(nil, "demo.go", []byte(goFile))
	if mode != fragModeOverlap {
		t.Errorf("nil Code: frag_mode %q, want %s", mode, fragModeOverlap)
	}
	if got, want := docFrags(t, s, "demo.go"), docFrags(t, ref, "demo.go"); !reflect.DeepEqual(got, want) {
		t.Errorf("nil Code changed the fragments:\n got %+v\nwant %+v", got, want)
	}
	if k := (&Worker{}).route(&Job{URL: "demo.go"}, Fetched{Data: []byte(goFile)}); k != KindUnknown {
		t.Errorf("nil Code routes demo.go to %s, want unknown", k)
	}
	if k := (&Worker{}).route(&Job{URL: "doc.md"}, Fetched{Data: []byte("# A")}); k != KindText {
		t.Errorf("nil Code routes doc.md to %s, want text", k)
	}

	s, mode = ingest(&fakeCode{symbols: goSpans}, "demo.go", []byte(goFile))
	if mode != fragModeCode {
		t.Errorf("with Code: frag_mode %q, want %s", mode, fragModeCode)
	}
	if got := docFrags(t, s, "demo.go"); len(got) != 4 || got[1].Symbol != "Hello" {
		t.Errorf("with Code: fragments %+v", got)
	}
	// A file the segmenter does not take stays text.
	if k := (&Worker{Code: &fakeCode{}}).route(&Job{URL: "notes.txt"}, Fetched{Data: []byte("x")}); k != KindText {
		t.Errorf("notes.txt routes to %s, want text", k)
	}
	// Minified code is still refused, as it is without a segmenter.
	minified := []byte("var a=" + strings.Repeat("1+", minifiedLineBytes) + "1;\n")
	if _, mode := ingest(&fakeCode{symbols: goSpans}, "min.go", minified); mode != "" {
		t.Errorf("minified .go was indexed with frag_mode %q", mode)
	}
}

// The segmenter is in the pool key of KindCode only: the other kinds never
// reach it, and re-keying them would re-read every PDF and image for nothing.
func TestPoolRecipe_SegmenterTerm(t *testing.T) {
	pool, err := OpenPool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	plain := NewWorker(testStore(t), Models{OCR: noTesseract, Channels: NewChannels()}, pool)
	coded := NewWorker(testStore(t), Models{OCR: noTesseract, Channels: NewChannels(), Code: &fakeCode{}}, pool)
	bumped := NewWorker(testStore(t), Models{OCR: noTesseract, Channels: NewChannels(), Code: &fakeCode{runtime: "fake@2"}}, pool)
	if plain.Code != nil || coded.Code == nil {
		t.Fatal("NewWorker did not carry Models.Code")
	}
	if plain.RecipeHash != coded.RecipeHash {
		t.Error("turning the segmenter on changed the recipe hash of every kind")
	}
	for _, k := range []DocKind{KindText, KindPDF, KindImage, KindOffice, KindEmail, KindSpreadsheet, KindUnknown} {
		if plain.poolRecipe(k) != coded.poolRecipe(k) || coded.poolRecipe(k) != bumped.poolRecipe(k) {
			t.Errorf("%s: pool key depends on the segmenter", k)
		}
	}
	if coded.poolRecipe(KindCode) == bumped.poolRecipe(KindCode) {
		t.Error("a new segmenter runtime kept the KindCode pool key")
	}
	if coded.poolRecipe(KindCode) == coded.poolRecipe(KindText) {
		t.Error("KindCode and KindText share a pool key")
	}
}

// countingTokenizer counts its requests.
type countingTokenizer struct {
	fakeTokenizer
	calls int
}

func (c *countingTokenizer) CountTokens(ctx context.Context, text string) (int, bool) {
	c.calls++
	return c.fakeTokenizer.CountTokens(ctx, text)
}

// Pieces far under the token limit are not sent to the tokenizer: the code
// fragmenter checks each piece more than once, and each check was two
// requests to the embed endpoint.
func TestIngestCode_ShortPiecesNotCounted(t *testing.T) {
	tok := &countingTokenizer{fakeTokenizer: fakeTokenizer{charsPerToken: 1}}
	fc := FragConfig{EmbedLimitTokens: 1000, EmbedTokenCounter: tok}
	s := openMem(t)
	if _, _, err := s.ingestCode(context.Background(), &fakeCode{symbols: goSpans}, "demo.go", "demo",
		[]byte(goFile), fc, nil); err != nil {
		t.Fatal(err)
	}
	// One probe, when the budget is built.
	if tok.calls != 1 {
		t.Errorf("%d tokenizer request(s) for a %d-byte file under a 1000-token limit, want 1", tok.calls, len(goFile))
	}
}
