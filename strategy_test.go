package raglit

import "testing"

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
