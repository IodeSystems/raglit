package raglit

import (
	"image"
	"image/color"
	"testing"
)

// draw returns a white canvas with black bars of the given width fraction,
// spaced like text lines.
func linesImage(w, h int, frac float64, n int) image.Image {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.White)
		}
	}
	gap := h / (n + 1)
	for i := 1; i <= n; i++ {
		y := i * gap
		for dy := 0; dy < 3; dy++ {
			// CHARACTERS, not a bar: 3 on, 2 off. A solid run would be a ruled
			// line, and LineSpan is right to reject those — a border is the
			// widest thing on a page and would make every crop look like prose.
			for x := 0; x < int(float64(w)*frac); x++ {
				if x%5 < 3 {
					m.Set(x, y+dy, color.Black)
				}
			}
		}
	}
	return m
}

// Running text must be cut in ROWS ONLY: a line crossing the crop cannot survive
// a vertical seam, and the model transcribes the fragment rather than noticing.
func TestFullWidthLinesForbidColumns(t *testing.T) {
	prose := LineSpan(linesImage(400, 400, 0.95, 20))
	if prose < proseLineSpan {
		t.Fatalf("full-width lines must measure as prose, got %.2f", prose)
	}
	cols, rows := gridFor(9, 27, 36.7, prose)
	if cols != 1 {
		t.Errorf("prose must not be cut into columns, got %dx%d", cols, rows)
	}
	if rows < 2 {
		t.Errorf("prose must still be cut into rows, got %d", rows)
	}
}

// Scattered labels — a drawing — keep their columns. This is the case tiling
// exists for, and the case where a seam severs nothing.
func TestScatteredInkKeepsColumns(t *testing.T) {
	sparse := LineSpan(linesImage(400, 400, 0.15, 20))
	if sparse >= proseLineSpan {
		t.Fatalf("short scattered ink must not measure as prose, got %.2f", sparse)
	}
	if cols, _ := gridFor(9, 27, 36.7, sparse); cols < 2 {
		t.Errorf("a drawing must keep its columns, got %d", cols)
	}
}

// Unmeasured must mean unmeasured. Treating 0 as "no lines" would silently
// narrow every crop the measurement could not read.
func TestUnmeasuredLineSpanKeepsExistingBehaviour(t *testing.T) {
	a, _ := gridFor(9, 27, 36.7, 0)
	b, _ := gridFor(9, 27, 36.7, 0.1)
	if a != b {
		t.Errorf("lineSpan 0 must behave as before, got %d vs %d", a, b)
	}
}

// A ruled line or a black margin is not a text line — it would otherwise be the
// widest thing on the page and force every crop to look like prose.
func TestSolidBarsAreNotTextLines(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 400, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 400; x++ {
			c := color.Color(color.White)
			if y > 100 && y < 300 { // a wide solid block, not text
				c = color.Black
			}
			m.Set(x, y, c)
		}
	}
	if s := LineSpan(m); s >= proseLineSpan {
		t.Errorf("a solid block must not read as running text, got %.2f", s)
	}
}
