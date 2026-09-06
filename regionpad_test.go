package raglit

import (
	"math"
	"testing"
)

// The pad must be the same DISTANCE regardless of how small the region is. The
// bug it replaces scaled with the region, so it vanished exactly where a clipped
// word hurts most.
func TestPadIsAConstantDistance(t *testing.T) {
	const w, h = 27.0, 36.72 // the survey sheet
	big := Rect{X: 0.1, Y: 0.1, W: 0.5, H: 0.5}
	tiny := Rect{X: 0.4, Y: 0.4, W: 0.02, H: 0.02}

	bp := big.paddedIn(descentPadIn, w, h)
	tp := tiny.paddedIn(descentPadIn, w, h)

	bGrewIn := (bp.W - big.W) * w / 2
	tGrewIn := (tp.W - tiny.W) * w / 2
	if math.Abs(bGrewIn-tGrewIn) > 1e-9 {
		t.Fatalf("pad differs by region size: big grew %.4fin, tiny grew %.4fin", bGrewIn, tGrewIn)
	}
	if math.Abs(bGrewIn-descentPadIn) > 1e-9 {
		t.Errorf("pad = %.4fin, want %.4fin", bGrewIn, descentPadIn)
	}
}

// The old fractional pad on a 2%-tall region of this sheet was ~0.022in against
// 6pt text at ~0.083in — a quarter of one character. Assert we clear a character.
func TestPadClearsACharacterOnASmallRegion(t *testing.T) {
	const w, h = 27.0, 36.72
	tiny := Rect{X: 0.5, Y: 0.5, W: 0.2, H: 0.02}
	got := (tiny.paddedIn(descentPadIn, w, h).H - tiny.H) * h / 2
	const sixPointIn = 6.0 / 72.0
	if got < sixPointIn {
		t.Fatalf("pad %.4fin does not clear one 6pt line (%.4fin)", got, sixPointIn)
	}
}

// Clamped, and a degenerate page cannot produce a garbage rect.
func TestPadClampsAndTolerates(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 1, H: 1}.paddedIn(descentPadIn, 8.5, 11)
	if r.X < 0 || r.Y < 0 || r.X+r.W > 1.000001 || r.Y+r.H > 1.000001 {
		t.Errorf("pad escaped the unit square: %+v", r)
	}
	same := Rect{X: 0.2, Y: 0.2, W: 0.3, H: 0.3}
	if got := same.paddedIn(descentPadIn, 0, 0); got != same {
		t.Errorf("zero page size should pass the rect through, got %+v", got)
	}
}
