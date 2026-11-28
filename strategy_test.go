package raglit

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The property that makes this section safe to add to an existing config: with
// nothing declared, every knob resolves to what the code did before. A read
// policy whose defaults are not the current behavior turns an upgrade into a
// bill, and this governs model spend.
func TestStrategyZeroValueIsTodaysBehavior(t *testing.T) {
	var c Config
	s := c.StrategyFor("anything")
	if s.Descend != 0 || s.Tile || s.AutoDescend || s.MaxCalls != 0 || s.Hint != "" {
		t.Errorf("zero config must yield the zero strategy, got %+v", s)
	}
	r := s.Render.resolved()
	if r.BaseDPI != baseRenderDPI || r.MaxDPI != maxRenderDPI ||
		r.TargetGlyphPx != targetGlyphPx || r.SmallTextGlyphPx != smallTextGlyphPx {
		t.Errorf("unset render policy must equal the package defaults, got %+v", r)
	}
}

// Per-index selection is the point of the feature: one corpus is not one kind of
// page, and the index is where document KIND is already declared.
func TestStrategyForPrefersIndexOverProjectDefault(t *testing.T) {
	c := Config{
		OCR: OCRConfig{
			Strategy: "flat",
			Strategies: map[string]StrategyConfig{
				"flat":   {Descend: 0},
				"survey": {Descend: 2, Tile: true, MaxCalls: 40, Hint: "monument calls"},
			},
		},
		Indexes: map[string]IndexConfig{
			"records":        {OCRStrategy: "survey"},
			"correspondence": {},
		},
	}
	if got := c.StrategyFor("records"); got.Descend != 2 || !got.Tile || got.Hint != "monument calls" {
		t.Errorf("records must get the survey strategy, got %+v", got)
	}
	// No per-index choice falls through to the project default, not to survey.
	if got := c.StrategyFor("correspondence"); got.Descend != 0 || got.Tile {
		t.Errorf("correspondence must fall through to the project default, got %+v", got)
	}
	// An index nobody declared behaves like one that declared nothing.
	if got := c.StrategyFor("no-such-index"); got.Descend != 0 {
		t.Errorf("unknown index must get the project default, got %+v", got)
	}
}

// A typo'd strategy name must not stop an ingest, and must not silently resolve
// to the project default either — that would hide the typo behind output that
// looks reasonable. It degrades to the zero value, and StrategyNamed is how a
// caller that wants to complain finds out.
func TestUnknownStrategyDegradesAndIsReportable(t *testing.T) {
	c := Config{
		OCR:     OCRConfig{Strategy: "flat", Strategies: map[string]StrategyConfig{"flat": {Descend: 3}}},
		Indexes: map[string]IndexConfig{"records": {OCRStrategy: "sruvey"}}, // typo
	}
	if got := c.StrategyFor("records"); got.Descend != 0 {
		t.Errorf("a typo'd strategy must not inherit the project default, got %+v", got)
	}
	if _, ok := c.StrategyNamed("sruvey"); ok {
		t.Error("StrategyNamed must report an unknown name")
	}
	if _, ok := c.StrategyNamed(""); ok {
		t.Error("StrategyNamed must report the empty name as unknown")
	}
	if s, ok := c.StrategyNamed("flat"); !ok || s.Descend != 3 {
		t.Errorf("StrategyNamed must find a declared strategy, got %+v ok=%v", s, ok)
	}
}

// A page measuring a clause's predicate follows that clause's policy, and a
// page measuring none follows the block exactly.
func TestStrategyForPageSelectsClausePolicy(t *testing.T) {
	block := StrategyConfig{
		Descend: 0, Tile: false, Hint: "block hint",
		MaxCalls: 8, MaxChildren: 2, MaxTransforms: 40, MaxEscalations: 4, MinRegionIn: 0.5,
		When: []StrategyWhenClause{
			{LowResolution: true, Policy: StrategyPolicy{Descend: 2, Tile: true, Hint: "monument calls"}},
		},
	}
	low := block.StrategyForPage(PageMeasure{LowResolution: true})
	if low.Descend != 2 || !low.Tile || low.Hint != "monument calls" {
		t.Errorf("low-resolution page must follow the clause's policy, got %+v", low)
	}
	none := block.StrategyForPage(PageMeasure{})
	if !reflect.DeepEqual(none, block) {
		t.Errorf("a page measuring no predicate must follow the block exactly, got %+v", none)
	}
}

// A page measuring no clause's predicate falls to the block default, field for
// field.
func TestStrategyForPageNoMatchFallsToBlockDefault(t *testing.T) {
	block := StrategyConfig{
		Descend: 1, Tile: true, Hint: "block",
		MaxCalls: 5, MaxChildren: 3, MaxTransforms: 20, MaxEscalations: 2, MinRegionIn: 1,
		Render:      RenderPolicy{BaseDPI: 150},
		AutoDescend: true,
		When: []StrategyWhenClause{
			{RuledTable: true, Policy: StrategyPolicy{Descend: 3, Hint: "ruled"}},
			{MultiColumn: true, Policy: StrategyPolicy{Descend: 4, Tile: true}},
		},
	}
	got := block.StrategyForPage(PageMeasure{LowResolution: true})
	if !reflect.DeepEqual(got, block) {
		t.Errorf("a page measuring none of the predicates must equal the block, got %+v", got)
	}
}

// No clauses is the live default: the resolved policy is byte-for-byte the
// block, whatever the page measures.
func TestStrategyForPageNoClausesIsUnchanged(t *testing.T) {
	block := StrategyConfig{
		Descend: 2, Tile: true, Hint: "monument calls",
		MaxCalls: 40, MaxChildren: 1, MaxTransforms: 10, MaxEscalations: 1, MinRegionIn: 2,
		Render:      RenderPolicy{BaseDPI: 300, MaxDPI: 900},
		AutoDescend: true,
	}
	for _, m := range []PageMeasure{{}, {LowResolution: true}, {RuledTable: true, MultiColumn: true}} {
		if got := block.StrategyForPage(m); !reflect.DeepEqual(got, block) {
			t.Errorf("no-clause block must resolve to itself for measure %+v, got %+v", m, got)
		}
	}
}

// A page measuring several predicates follows the first written clause, and the
// choice is deterministic.
func TestStrategyForPageFirstWrittenClauseWins(t *testing.T) {
	both := PageMeasure{LowResolution: true, RuledTable: true, MultiColumn: true}
	lo := StrategyPolicy{Descend: 2, Hint: "low"}
	ru := StrategyPolicy{Descend: 4, Hint: "ruled"}
	firstLow := StrategyConfig{
		When: []StrategyWhenClause{
			{LowResolution: true, Policy: lo},
			{RuledTable:    true, Policy: ru},
		},
	}
	for i := 0; i < 2; i++ {
		if got := firstLow.StrategyForPage(both); got.Hint != "low" || got.Descend != 2 {
			t.Fatalf("run %d: low-resolution first must win, got %+v", i, got)
		}
	}
	firstRuled := StrategyConfig{
		When: []StrategyWhenClause{
			{RuledTable:    true, Policy: ru},
			{LowResolution: true, Policy: lo},
		},
	}
	for i := 0; i < 2; i++ {
		if got := firstRuled.StrategyForPage(both); got.Hint != "ruled" || got.Descend != 4 {
			t.Fatalf("run %d: ruled-table first must win, got %+v", i, got)
		}
	}
}

// A clause selects a policy, never a budget: every budget field of the
// resolved page comes from the block.
func TestStrategyForPageBudgetsStayOnBlock(t *testing.T) {
	block := StrategyConfig{
		MaxCalls: 5, MaxChildren: 3, MaxTransforms: 20, MaxEscalations: 2, MinRegionIn: 1,
		When: []StrategyWhenClause{
			{RuledTable: true, Policy: StrategyPolicy{Descend: 3, Hint: "ruled"}},
		},
	}
	got := block.StrategyForPage(PageMeasure{RuledTable: true})
	if got.MaxCalls != 5 || got.MaxChildren != 3 || got.MaxTransforms != 20 ||
		got.MaxEscalations != 2 || got.MinRegionIn != 1 {
		t.Errorf("budgets must stay on the block, got %+v", got)
	}
}

// The selection's inputs carry no model-detected kind: PageMeasure is built
// from pixels-only fields, StrategyWhenClause carries no budget fields, and the
// selector names no drawing-kind gate — the one F10 removed.
func TestStrategyForPageNamesNoModelDetectedKind(t *testing.T) {
	// Built from pixels-only fields; selection works with them and nothing else.
	b := StrategyConfig{
		When: []StrategyWhenClause{{MultiColumn: true, Policy: StrategyPolicy{Hint: "columns"}}},
	}
	if got := b.StrategyForPage(PageMeasure{MultiColumn: true}); got.Hint != "columns" {
		t.Errorf("pixels-only measure must drive selection, got %+v", got)
	}
	if src, err := os.ReadFile("config.go"); err == nil {
		src := string(src)
		fn := ""
		if i := strings.Index(src, "func (b StrategyConfig) StrategyForPage("); i >= 0 {
			fn = src[i:]
			if j := strings.Index(fn, "\n}\n"); j >= 0 {
				fn = fn[:j]
			}
		}
		if fn == "" {
			t.Fatal("StrategyForPage not found in config.go")
		}
		if strings.Contains(strings.ToLower(fn), "kind") || strings.Contains(strings.ToLower(fn), "drawing") {
			t.Errorf("the selection must name no model-detected kind:\n%s", fn)
		}
	}
}

// Each render field resolves independently — setting one must not reset the
// others to defaults, which is the usual way a partial override goes wrong.
func TestRenderPolicyResolvesFieldwise(t *testing.T) {
	r := RenderPolicy{MaxDPI: 400}.resolved()
	if r.MaxDPI != 400 {
		t.Errorf("explicit MaxDPI must survive, got %d", r.MaxDPI)
	}
	if r.BaseDPI != baseRenderDPI || r.TargetGlyphPx != targetGlyphPx || r.SmallTextGlyphPx != smallTextGlyphPx {
		t.Errorf("unset fields must keep defaults, got %+v", r)
	}
	full := RenderPolicy{BaseDPI: 150, MaxDPI: 300, TargetGlyphPx: 24, SmallTextGlyphPx: 10}.resolved()
	if full != (RenderPolicy{BaseDPI: 150, MaxDPI: 300, TargetGlyphPx: 24, SmallTextGlyphPx: 10}) {
		t.Errorf("a fully specified policy must pass through unchanged, got %+v", full)
	}
}
