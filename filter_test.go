package raglit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// filterFixture is two documents where the one that does NOT qualify ranks
// higher on every signal: "loud.md" is all query term, "quiet.md" is the query
// term diluted. Only quiet.md carries tags and fields, so a filtered limit-1
// query returns it only if the filter is applied before the limit.
func filterFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "loud.md", Title: "Loud", Fragments: []Fragment{
		{Text: "token token token"}}}))
	must(t, s.Ingest(ctx, Document{Path: "quiet.md", Title: "Quiet", Fragments: []Fragment{
		{Text: "token deploy rollback with several other filler words in between"}}}))
	must(t, s.SetDocumentIdentity(ctx, "quiet.md", DocIdentity{
		Kind:        "guide",
		ContentTags: []string{"lead paint", "survey", "boundary line"},
		RoleTags:    []string{"reference"},
	}))
	must(t, s.SetDocumentFields(ctx, "quiet.md", DocFields{Type: "inspection",
		Fields: json.RawMessage(`{"county":"Kent","floors":5,"site.lot":"7a","rooms":["a"]}`)}))
	must(t, s.SetTags("quiet.md", []string{"Kind:Strategy", "pinned"}))
}

func hitPaths(hs []Hit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Path)
	}
	return out
}

// filterCases are shared by the FTS, vector and hybrid tests: each filter and
// whether quiet.md (the only document with tags or fields) should qualify.
var filterCases = []struct {
	name string
	f    SearchFilter
	want bool
}{
	{"content tag", SearchFilter{Tags: []string{"lead paint"}}, true},
	{"role tag", SearchFilter{Tags: []string{"reference"}}, true},
	{"tag normalized", SearchFilter{Tags: []string{"  Lead   PAINT "}}, true},
	{"tags AND, all held", SearchFilter{Tags: []string{"survey", "reference"}}, true},
	{"tags AND, one missing", SearchFilter{Tags: []string{"survey", "invoice"}}, false},
	{"partial tag is not a tag", SearchFilter{Tags: []string{"paint"}}, false},
	{"tag prefix is not a tag", SearchFilter{Tags: []string{"lead"}}, false},
	{"caller tag", SearchFilter{Tags: []string{"kind:strategy"}}, true},
	{"caller tag normalized", SearchFilter{Tags: []string{" KIND:Strategy "}}, true},
	{"caller tag longer is not a tag", SearchFilter{Tags: []string{"kind:strategyx"}}, false},
	{"caller tag prefix is not a tag", SearchFilter{Tags: []string{"kind"}}, false},
	{"caller, content and role tags AND", SearchFilter{Tags: []string{"pinned", "survey", "reference"}}, true},
	{"caller tag AND missing", SearchFilter{Tags: []string{"pinned", "invoice"}}, false},
	{"empty tag ignored", SearchFilter{Tags: []string{"  "}}, true},
	{"field equal", SearchFilter{Fields: map[string]string{"county": "Kent"}}, true},
	{"field differs", SearchFilter{Fields: map[string]string{"county": "Essex"}}, false},
	{"field key with a dot", SearchFilter{Fields: map[string]string{"site.lot": "7a"}}, true},
	{"number field is not a string", SearchFilter{Fields: map[string]string{"floors": "5"}}, false},
	{"array field is not a string", SearchFilter{Fields: map[string]string{"rooms": `["a"]`}}, false},
	{"absent field", SearchFilter{Fields: map[string]string{"owner": "x"}}, false},
	{"tag and field", SearchFilter{Tags: []string{"survey"}, Fields: map[string]string{"county": "Kent"}}, true},
	{"path excludes", SearchFilter{PathPrefix: "loud", Tags: []string{"survey"}}, false},
	// New single-valued fields: kind, doc_type, origin, role_tag, content_tag.
	// quiet.md has Kind "guide" (set in filterFixture), DocType "inspection"
	// (set via SetDocumentFields), RoleTag "reference", ContentTag "lead paint".
	// loud.md has none of these set, so a no-match case returns nothing.
	{"kind match", SearchFilter{Kind: "guide"}, true},
	{"kind no-match", SearchFilter{Kind: "reference"}, false},
	{"doc_type match", SearchFilter{DocType: "inspection"}, true},
	{"doc_type no-match", SearchFilter{DocType: "runbook"}, false},
	{"origin no-match (fixture has no identity fragments)", SearchFilter{Origin: "identity"}, false},
	{"origin none", SearchFilter{Origin: "nonexistent"}, false},
	{"role_tag whole", SearchFilter{RoleTag: "reference"}, true},
	{"role_tag partial", SearchFilter{RoleTag: "ref"}, false},
	{"content_tag whole", SearchFilter{ContentTag: "lead paint"}, true},
	{"content_tag partial", SearchFilter{ContentTag: "paint"}, false},
}

// check asserts a limit-1 filtered result: quiet.md alone when it qualifies,
// loud.md alone when the filter is a no-op, nothing otherwise.
func checkFiltered(t *testing.T, name string, f SearchFilter, want bool, hits []Hit) {
	t.Helper()
	got := hitPaths(hits)
	noop := len(f.Fields) == 0 && f.PathPrefix == "" &&
		f.Kind == "" && f.DocType == "" && f.Origin == "" &&
		f.RoleTag == "" && f.ContentTag == "" &&
		(len(f.Tags) == 0 || normalizeContentTag(f.Tags[0]) == "")
	switch {
	case noop:
		if len(got) != 1 || got[0] != "loud.md" {
			t.Errorf("%s: no-op filter, limit 1 = %v, want [loud.md]", name, got)
		}
	case want:
		if len(got) != 1 || got[0] != "quiet.md" {
			t.Errorf("%s: limit 1 = %v, want [quiet.md]", name, got)
		}
	default:
		if len(got) != 0 {
			t.Errorf("%s: = %v, want nothing", name, got)
		}
	}
}

func TestSearchFiltered_FTS(t *testing.T) {
	s := openMem(t)
	filterFixture(t, s)
	// The premise: unfiltered, the non-qualifying document ranks first.
	if h, _ := s.Search("token", 1); len(h) != 1 || h[0].Path != "loud.md" {
		t.Fatalf("fixture: unfiltered top hit = %v, want loud.md", hitPaths(h))
	}
	for _, c := range filterCases {
		h, err := s.SearchFiltered("token", c.f, 1)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		checkFiltered(t, c.name, c.f, c.want, h)
	}
}

func TestSearchFiltered_Vector(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	filterFixture(t, s)
	ctx := context.Background()
	if h, _ := s.VecSearch(ctx, "token", 1); len(h) != 1 || h[0].Path != "loud.md" {
		t.Fatalf("fixture: unfiltered top hit = %v, want loud.md", hitPaths(h))
	}
	for _, c := range filterCases {
		h, err := s.VecSearchFiltered(ctx, "token", c.f, 1)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		checkFiltered(t, c.name, c.f, c.want, h)
	}
}

func TestSearchFiltered_Hybrid(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	filterFixture(t, s)
	ctx := context.Background()
	if h, _ := s.HybridSearch(ctx, "token", 1); len(h) != 1 || h[0].Path != "loud.md" {
		t.Fatalf("fixture: unfiltered top hit = %v, want loud.md", hitPaths(h))
	}
	for _, c := range filterCases {
		h, err := s.HybridSearchFiltered(ctx, "token", c.f, 1)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		checkFiltered(t, c.name, c.f, c.want, h)
	}
}

func TestSearchFiltered_BadFieldKey(t *testing.T) {
	s := openMem(t)
	filterFixture(t, s)
	for _, k := range []string{"", `a"b`} {
		if _, err := s.SearchFiltered("token", SearchFilter{Fields: map[string]string{k: "x"}}, 1); err == nil {
			t.Errorf("key %q: want an error", k)
		}
	}
}

// A branch that changes a document's fields or tags has its own copy of the
// document, and that copy shadows the parent's — so the parent's stale values
// cannot answer a filtered search through the branch. The branch cannot edit a
// parent-only document's fields or identity at all: the write needs the row in
// the branch's own layer.
func TestSearchFiltered_BranchShadowsStaleParentValues(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	reg.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()
	main, _ := reg.Get("main")
	doc := Document{Path: "file:///edited.md", Title: "E", Fragments: []Fragment{{Text: "token deploy"}}}
	must(t, main.Ingest(ctx, doc))
	must(t, main.SetDocumentFields(ctx, doc.Path, DocFields{Type: "t", Fields: json.RawMessage(`{"county":"Kent"}`)}))
	must(t, main.SetDocumentIdentity(ctx, doc.Path, DocIdentity{ContentTags: []string{"old tag"}, RoleTags: []string{"reference"}}))
	must(t, main.Ingest(ctx, Document{Path: "file:///untouched.md", Title: "U", Fragments: []Fragment{{Text: "token rollback"}}}))
	must(t, main.SetDocumentFields(ctx, "file:///untouched.md", DocFields{Type: "t", Fields: json.RawMessage(`{"county":"Kent"}`)}))

	must(t, reg.ForkBranch("feat", "main"))
	feat, _ := reg.Get("feat")
	if err := feat.SetDocumentFields(ctx, doc.Path, DocFields{Type: "t", Fields: json.RawMessage(`{"county":"Essex"}`)}); err == nil {
		t.Fatal("a branch wrote fields for a document it has no copy of")
	}
	// The edit in the branch: its own copy, with new fields and tags.
	must(t, feat.Ingest(ctx, doc))
	must(t, feat.SetDocumentFields(ctx, doc.Path, DocFields{Type: "t", Fields: json.RawMessage(`{"county":"Essex"}`)}))
	must(t, feat.SetDocumentIdentity(ctx, doc.Path, DocIdentity{ContentTags: []string{"new tag"}, RoleTags: []string{"reference"}}))

	type search func(SearchFilter) ([]Hit, error)
	modes := map[string]func(*Store) search{
		"fts": func(s *Store) search {
			return func(f SearchFilter) ([]Hit, error) { return s.SearchFiltered("token", f, 10) }
		},
		"vec": func(s *Store) search {
			return func(f SearchFilter) ([]Hit, error) { return s.VecSearchFiltered(ctx, "token", f, 10) }
		},
		"hybrid": func(s *Store) search {
			return func(f SearchFilter) ([]Hit, error) { return s.HybridSearchFiltered(ctx, "token", f, 10) }
		},
	}
	kent := SearchFilter{Fields: map[string]string{"county": "Kent"}}
	essex := SearchFilter{Fields: map[string]string{"county": "Essex"}}
	oldTag := SearchFilter{Tags: []string{"old tag"}}
	newTag := SearchFilter{Tags: []string{"new tag"}}
	eq := func(got []string, want ...string) bool {
		m := map[string]bool{}
		for _, p := range got {
			m[p] = true
		}
		if len(m) != len(want) {
			return false
		}
		for _, w := range want {
			if !m[w] {
				return false
			}
		}
		return true
	}
	for name, mode := range modes {
		cases := []struct {
			s    *Store
			f    SearchFilter
			want []string
		}{
			// Through the branch: the parent's Kent copy of edited.md is shadowed;
			// the parent-only document still shows through, filtered in its layer.
			{feat, kent, []string{"file:///untouched.md"}},
			{feat, essex, []string{"file:///edited.md"}},
			{feat, oldTag, nil},
			{feat, newTag, []string{"file:///edited.md"}},
			// The parent is untouched.
			{main, kent, []string{"file:///edited.md", "file:///untouched.md"}},
			{main, essex, nil},
			{main, oldTag, []string{"file:///edited.md"}},
		}
		for i, c := range cases {
			h, err := mode(c.s)(c.f)
			if err != nil {
				t.Fatalf("%s #%d: %v", name, i, err)
			}
			if got := hitPaths(h); !eq(got, c.want...) {
				t.Errorf("%s #%d: = %v, want %v", name, i, got, c.want)
			}
		}
	}
}

// Tags written through SetDocumentIdentity arrive raw — a person's correction,
// an embedder's own tags — and are normalized on write, so a filter for the tag
// matches however irregularly it was spelled.
func TestSearchFiltered_RawTagsNormalizedOnWrite(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "raw.md", Title: "Raw", Fragments: []Fragment{{Text: "token"}}}))
	must(t, s.SetDocumentIdentity(ctx, "raw.md", DocIdentity{
		ContentTags: []string{"Lead  Paint", " survey", "v1.", "a;b", "  "},
		RoleTags:    []string{" Reference "},
	}))
	d, err := s.DocumentIdentity("raw.md")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(d.ContentTags, "|"), "lead paint|survey|v1|a b"; got != want {
		t.Errorf("stored content tags = %q, want %q", got, want)
	}
	for _, tag := range []string{"lead paint", "survey", "v1", "a b", "reference", "V1 ."} {
		h, err := s.SearchFiltered("token", SearchFilter{Tags: []string{tag}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := hitPaths(h); len(got) != 1 || got[0] != "raw.md" {
			t.Errorf("tag %q: = %v, want [raw.md]", tag, got)
		}
	}
}

// fieldQueryFixture covers every coercion case under one field, "amount": a
// JSON number (a.md), a numeric string (c.md), a non-numeric string (d.md), a
// string that begins with a number (e.md) and an array (f.md) — with "county"
// as the equality field. b.md has no extraction at all.
func fieldQueryFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, p := range []string{"a.md", "b.md", "c.md", "d.md", "e.md", "f.md"} {
		must(t, s.Ingest(ctx, Document{Path: p, Title: p, Fragments: []Fragment{{Text: "token"}}}))
	}
	must(t, s.SetDocumentFields(ctx, "a.md", DocFields{Type: "workorder",
		Fields: json.RawMessage(`{"amount":500,"county":"Kent"}`)}))
	must(t, s.SetDocumentFields(ctx, "c.md", DocFields{Type: "workorder",
		Fields: json.RawMessage(`{"amount":"750.5","county":"Kent"}`)}))
	must(t, s.SetDocumentFields(ctx, "d.md", DocFields{Type: "workorder",
		Fields: json.RawMessage(`{"amount":"over five hundred","county":"Essex"}`)}))
	must(t, s.SetDocumentFields(ctx, "e.md", DocFields{Type: "workorder",
		Fields: json.RawMessage(`{"amount":"5 apples","county":"Essex"}`)}))
	must(t, s.SetDocumentFields(ctx, "f.md", DocFields{Type: "workorder",
		Fields: json.RawMessage(`{"amount":[1,2],"county":"Kent"}`)}))
}

func fieldDocPaths(ds []FieldDoc) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Path)
	}
	return out
}

func TestFieldQueryDocs_Equality(t *testing.T) {
	s := openMem(t)
	fieldQueryFixture(t, s)
	ds, err := s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
		{Field: "county", Op: FieldEq, Value: "Kent"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Path order: a, c, f. b has no extraction and d, e have a different county.
	if got, want := fieldDocPaths(ds), []string{"a.md", "c.md", "f.md"}; !stringsEqual(got, want) {
		t.Fatalf("eq county=Kent = %v, want %v", got, want)
	}
	// A stored JSON number is not equal to its spelling: the equality rule is
	// the SearchFilter rule — only a stored string matches.
	ds, err = s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
		{Field: "amount", Op: FieldEq, Value: "500"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldDocPaths(ds); len(got) != 0 {
		t.Fatalf("eq amount=500 (a stored number) = %v, want nothing", got)
	}
	// A stored string equals its value.
	ds, err = s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
		{Field: "amount", Op: FieldEq, Value: "over five hundred"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldDocPaths(ds); !stringsEqual(got, []string{"d.md"}) {
		t.Fatalf("eq amount=over five hundred = %v, want [d.md]", got)
	}
}

func TestFieldQueryDocs_Comparisons(t *testing.T) {
	s := openMem(t)
	fieldQueryFixture(t, s)
	cases := []struct {
		op   FieldOp
		want []string
	}{
		{FieldGt, []string{"c.md"}},          // 750.5 > 500; 500 is not
		{FieldGte, []string{"a.md", "c.md"}}, // 500 and 750.5
		{FieldLt, []string{}},                // 500 < 500 is false
		{FieldLte, []string{"a.md"}},         // 500 <= 500
	}
	for _, c := range cases {
		ds, err := s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
			{Field: "amount", Op: c.op, Value: "500"},
			{Field: "amount", Op: FieldLt, Value: "750.5"},
		}})
		// The second constraint ANDs in: drop c.md from each expectation.
		want := []string{}
		for _, p := range c.want {
			if p != "c.md" {
				want = append(want, p)
			}
		}
		if err != nil {
			t.Fatalf("%s: %v", c.op, err)
		}
		if got := fieldDocPaths(ds); !stringsEqual(got, want) {
			t.Errorf("%s AND lt 750.5 = %v, want %v", c.op, got, want)
		}
	}
	// A non-numeric string, a number-leading string and an array are all not
	// coercible: only a.md (500) and c.md (750.5) match.
	ds, err := s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
		{Field: "amount", Op: FieldGt, Value: "0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldDocPaths(ds); !stringsEqual(got, []string{"a.md", "c.md"}) {
		t.Fatalf("gt 0 = %v, want [a.md c.md] (d, e, f non-coercible out)", got)
	}
	// An absent key matches no comparison: b.md has no extraction, and a.md's
	// "county" is absent from the amount comparison below.
	ds, err = s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
		{Field: "missing", Op: FieldGt, Value: "0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldDocPaths(ds); len(got) != 0 {
		t.Fatalf("gt 0 on an absent key = %v, want nothing", got)
	}
	// A string that begins with a number is not coercible: "over five hundred"
	// must not compare as 0 (SQLite CAST would read the leading words as 0).
	ds, err = s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
		{Field: "amount", Op: FieldLte, Value: "0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldDocPaths(ds); len(got) != 0 {
		t.Fatalf("lte 0 = %v, want nothing (a non-numeric string is not 0)", got)
	}
}

func TestFieldQueryDocs_SortAndLimit(t *testing.T) {
	s := openMem(t)
	fieldQueryFixture(t, s)
	// Ascending: the coercible values in order, then the non-coercible
	// documents last, in path order.
	ds, err := s.FieldQueryDocs(FieldQuery{SortField: "amount"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fieldDocPaths(ds), []string{"a.md", "c.md", "d.md", "e.md", "f.md"}; !stringsEqual(got, want) {
		t.Fatalf("asc = %v, want %v (b.md has no extraction; d, e, f non-coercible last)", got, want)
	}
	// Descending: the coercible values reversed, absent-last kept.
	ds, err = s.FieldQueryDocs(FieldQuery{SortField: "amount", Dir: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fieldDocPaths(ds), []string{"c.md", "a.md", "d.md", "e.md", "f.md"}; !stringsEqual(got, want) {
		t.Fatalf("desc = %v, want %v (d, e, f non-coercible last in path order)", got, want)
	}
	// A sort on a field that is absent for every extracted document: all last,
	// path order.
	ds, err = s.FieldQueryDocs(FieldQuery{SortField: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fieldDocPaths(ds), []string{"a.md", "c.md", "d.md", "e.md", "f.md"}; !stringsEqual(got, want) {
		t.Fatalf("sort on an absent field = %v, want %v", got, want)
	}
	// Limit cuts the ordered result.
	ds, err = s.FieldQueryDocs(FieldQuery{SortField: "amount", Dir: "desc", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fieldDocPaths(ds), []string{"c.md", "a.md"}; !stringsEqual(got, want) {
		t.Fatalf("desc limit 2 = %v, want %v", got, want)
	}
}

func TestFieldQueryDocs_EmptyConstraints(t *testing.T) {
	s := openMem(t)
	fieldQueryFixture(t, s)
	ds, err := s.FieldQueryDocs(FieldQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// Every extracted document, in path order — b.md, which has no extraction,
	// is not in the result.
	if got, want := fieldDocPaths(ds), []string{"a.md", "c.md", "d.md", "e.md", "f.md"}; !stringsEqual(got, want) {
		t.Fatalf("empty = %v, want %v", got, want)
	}
}

func TestFieldQueryDocs_ResultCarriesFieldValues(t *testing.T) {
	s := openMem(t)
	fieldQueryFixture(t, s)
	ds, err := s.FieldQueryDocs(FieldQuery{Constraints: []FieldConstraint{
		{Field: "county", Op: FieldEq, Value: "Kent"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("got %d docs", len(ds))
	}
	for _, d := range ds {
		if d.Type != "workorder" {
			t.Errorf("%s: type = %q, want workorder", d.Path, d.Type)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(d.Fields), &m); err != nil {
			t.Fatalf("%s: fields not JSON: %v", d.Path, err)
		}
		if m["county"] != "Kent" {
			t.Errorf("%s: fields.county = %v, want Kent", d.Path, m["county"])
		}
	}
}

func TestFieldQueryDocs_BadInput(t *testing.T) {
	s := openMem(t)
	fieldQueryFixture(t, s)
	bad := []FieldQuery{
		{Constraints: []FieldConstraint{{Field: "", Op: FieldEq, Value: "x"}}},
		{Constraints: []FieldConstraint{{Field: `a"b`, Op: FieldEq, Value: "x"}}},
		{Constraints: []FieldConstraint{{Field: "county", Op: "bogus", Value: "x"}}},
		{SortField: `a"b`},
	}
	for i, q := range bad {
		if _, err := s.FieldQueryDocs(q); err == nil {
			t.Errorf("#%d: want an error", i)
		}
	}
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
