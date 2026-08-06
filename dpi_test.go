package raglit

import (
	"math"
	"testing"
)

// The identity the whole model rests on: 200 DPI IS the readable baseline that
// regions.go measured independently as 39 tokens per square inch. If this ever
// drifts, one of the two is wrong and every threshold downstream is guessing.
func TestTwoHundredDPIIsTheReadableBaseline(t *testing.T) {
	got := TokensPerSqInAt(200)
	if math.Abs(got-letterTokensPerSqIn) > 1.0 {
		t.Errorf("200 DPI = %.2f t/in², but regions.go's baseline is %.1f — these must agree",
			got, letterTokensPerSqIn)
	}
	// And the relationship is quadratic, not linear: doubling DPI quadruples cost.
	if q := TokensPerSqInAt(400) / TokensPerSqInAt(200); math.Abs(q-4) > 0.01 {
		t.Errorf("doubling dpi must quadruple tokens, got %.2fx", q)
	}
}

// The cap falls as the canvas grows — the bound that makes a big sheet
// unreadable whole at ANY setting. Numbers measured on the real corpus.
func TestDPICapFallsWithArea(t *testing.T) {
	for _, tc := range []struct {
		name string
		area float64
		want int
	}{
		// TRUNCATED, not rounded: the cap is a ceiling, and 423.6 rounded to 424
		// would ask for a resolution the server then downscales — quietly
		// reintroducing the crush this bound exists to predict.
		{"letter 8.5x11", 8.5 * 11, 423},
		{"1984 SWD 12.6x18.4", 12.6 * 18.4, 269},
		{"1947 replat 27.5x17.1", 27.5 * 17.1, 188},
		{"2008 E-size 27x36.7", 27 * 36.7, 130},
	} {
		if got := DPICapForArea(tc.area, DefaultMaxImageTokens); got != tc.want {
			t.Errorf("%s: cap = %d, want %d", tc.name, got, tc.want)
		}
	}
	// Monotone: a bigger sheet never permits a higher resolution.
	prev := math.MaxInt32
	for _, a := range []float64{50, 100, 250, 500, 1000} {
		got := DPICapForArea(a, DefaultMaxImageTokens)
		if got >= prev {
			t.Errorf("cap must fall with area: %v in² gave %d, previous %d", a, got, prev)
		}
		prev = got
	}
}

// A 200 DPI scan rendered at 400 is interpolation: more pixels, more tokens, not
// one more glyph. This is the bound with no upside to crossing, so it is a hard
// stop rather than a preference — the whole delano corpus is natively 200.
func TestNativeResolutionIsAHardStop(t *testing.T) {
	d := ChooseDPI(400, 8.5*11, 200, DefaultMaxImageTokens, RenderPolicy{})
	if d.DPI != 200 || d.Reason != "native" {
		t.Errorf("must clamp to the scan's own resolution, got %+v", d)
	}
	// Unknown native (born-digital, or no poppler) must NOT act as a bound —
	// zero would otherwise refuse to render the page at all.
	d = ChooseDPI(400, 8.5*11, 0, DefaultMaxImageTokens, RenderPolicy{})
	if d.DPI != 400 || d.Reason != "need" {
		t.Errorf("unknown native must not clamp, got %+v", d)
	}
}

// The cap is RECORDED but must not clamp the render: tiling is the answer to an
// oversized sheet. Clamping here would hand the reader a page below the readable
// baseline while reporting success, which is the failure mode that made MOWRER
// unreadable and looked like a model defect.
func TestCapIsReportedNotApplied(t *testing.T) {
	esize := 27 * 36.7
	d := ChooseDPI(200, esize, 200, DefaultMaxImageTokens, RenderPolicy{})
	if d.DPI != 200 {
		t.Errorf("cap must not clamp the render, got dpi=%d", d.DPI)
	}
	if d.CapDPI != 130 {
		t.Errorf("cap should be recorded as 130, got %d", d.CapDPI)
	}
	if d.Tiles < 2 {
		t.Errorf("an oversized sheet must ask for tiles, got %d", d.Tiles)
	}
}

// Tiling is the ONLY lever on a natively-200-DPI oversized scan, and the count
// is arithmetic: every pixel has to reach the model at native resolution.
func TestTilesNeededCoversEveryPixel(t *testing.T) {
	esize := 27 * 36.7 // 991 in², 39.7 MP at 200 dpi
	n := TilesNeeded(esize, 200, DefaultMaxImageTokens)
	if n < 3 {
		t.Errorf("991 in² at 200 dpi needs >=3 tiles to show every pixel, got %d", n)
	}
	// A letter page fits whole — tiling it would be pure cost.
	if n := TilesNeeded(8.5*11, 200, DefaultMaxImageTokens); n != 1 {
		t.Errorf("a letter page must need exactly 1 tile, got %d", n)
	}
	// More resolution means more tiles, quadratically.
	lo := TilesNeeded(esize, 200, DefaultMaxImageTokens)
	hi := TilesNeeded(esize, 400, DefaultMaxImageTokens)
	if hi <= lo {
		t.Errorf("raising dpi must raise the tile count: %d at 200, %d at 400", lo, hi)
	}
}

// Each tile is its own canvas, so cutting a sheet RAISES the resolution ceiling.
// That is the entire mechanism — not more pixels, a smaller canvas.
func TestTilingRaisesTheCeiling(t *testing.T) {
	full := 991.0
	for _, n := range []float64{4, 9, 16} {
		if tile, whole := DPICapForArea(full/n, DefaultMaxImageTokens), DPICapForArea(full, DefaultMaxImageTokens); tile <= whole {
			t.Errorf("1/%v tile cap %d must exceed the whole-sheet cap %d", n, tile, whole)
		}
	}
}
