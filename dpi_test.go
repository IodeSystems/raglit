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
		// With capHeadroom reserved for the prompt, so these are ~8% below the
		// naive sqrt: an image sized to the full budget goes over the moment an
		// instruction is added, and the server halves it.
		{"letter 8.5x11", 8.5 * 11, 390},
		{"1984 SWD 12.6x18.4", 12.6 * 18.4, 248},
		{"1947 replat 27.5x17.1", 27.5 * 17.1, 174},
		{"2008 E-size 27x36.7", 27 * 36.7, 119},
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
	// zero would otherwise refuse to render the page at all. Need is kept under
	// the area cap so this isolates NATIVE; the cap clamping is its own test.
	d = ChooseDPI(300, 8.5*11, 0, DefaultMaxImageTokens, RenderPolicy{})
	if d.DPI != 300 || d.Reason != "need" {
		t.Errorf("unknown native must not clamp, got %+v", d)
	}
}

// The cap IS applied. This reverses the original decision, on measurement:
// rendering above what the encoder accepts is not merely wasteful, it is worse.
// The server halves an oversized image until it fits, each pass costing detail —
// the same root read measured 9 tokens/in² rendered at 200 and 4 at 400.
// Tiling remains the answer for an oversized sheet; it is not an argument for
// rasterising past the ceiling first.
func TestCapIsApplied(t *testing.T) {
	esize := 27 * 36.7
	d := ChooseDPI(200, esize, 200, DefaultMaxImageTokens, RenderPolicy{})
	if d.DPI != 119 || d.Reason != "cap" {
		t.Errorf("cap must clamp the render, got %+v", d)
	}
	if d.Tiles < 2 {
		t.Errorf("an oversized sheet must still ask for tiles, got %d", d.Tiles)
	}
	// A page that fits is untouched by the cap.
	if d := ChooseDPI(300, 8.5*11, 0, DefaultMaxImageTokens, RenderPolicy{}); d.DPI != 300 {
		t.Errorf("a page under the cap must render at need, got %+v", d)
	}
}

// No glyph measurement must fall back to what IS known — the scan's own
// resolution — not to a constant. Returning BaseDPI here is what made the whole
// policy inert: no cheap engine is configured on this corpus, so EVERY page
// rendered at 200 whatever its scan held, and a 960 DPI survey certificate was
// read at 200 and lost.
func TestNativeFallbackWhenNothingMeasured(t *testing.T) {
	// The ROS: 94 in² letter sheet scanned at 960, cap 423.
	d := ChooseDPI(0, 8.5*11, 960, DefaultMaxImageTokens, RenderPolicy{})
	if d.DPI != d.CapDPI || d.DPI < 350 {
		t.Errorf("must rise to the cap on a high-native sheet, got %+v", d)
	}
	// With neither a measurement nor a native resolution there is nothing to go
	// on, and the base is the honest answer.
	if d := ChooseDPI(0, 8.5*11, 0, DefaultMaxImageTokens, RenderPolicy{}); d.DPI != baseRenderDPI {
		t.Errorf("no information at all must yield the base, got %+v", d)
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
