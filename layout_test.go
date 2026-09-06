package raglit

import (
	"image"
	"image/color"
	"testing"
)

// canvas is white; ink is drawn as dashes so a "line of text" has the word gaps
// that distinguish it from a drawn rule. A solid run IS a rule, and every
// measurement here depends on that difference being real in the fixture.
func canvas(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.White)
		}
	}
	return m
}

func textBlock(m *image.RGBA, x0, y0, w, h, lineGap int) {
	for y := y0; y < y0+h; y += lineGap {
		for dy := 0; dy < 3; dy++ {
			// WORDS of ~40px separated by ~20px gaps. The gap has to survive the
			// segmenter's 8px cell grid — real word spacing at 200 DPI is ~15px,
			// about two cells, and that is exactly what distinguishes a line of
			// text from a drawn rule. A fixture with 2px gaps has no word breaks
			// at cell resolution and is correctly erased as linework.
			for x := x0; x < x0+w; x++ {
				if (x-x0)%60 < 40 {
					m.Set(x, y+dy, color.Black)
				}
			}
		}
	}
}

func solidLine(m *image.RGBA, x0, y0, x1, y1 int) {
	if y0 == y1 {
		for x := x0; x <= x1; x++ {
			for d := 0; d < 3; d++ {
				m.Set(x, y0+d, color.Black)
			}
		}
		return
	}
	for y := y0; y <= y1; y++ {
		for d := 0; d < 3; d++ {
			m.Set(x0+d, y, color.Black)
		}
	}
}

// Two separated blocks must come back as two clusters, not one. This is the
// whole point: the gap between them is where cutting is safe.
func TestSeparateBlocksBecomeSeparateClusters(t *testing.T) {
	m := canvas(800, 800)
	textBlock(m, 40, 40, 300, 200, 20)
	textBlock(m, 460, 500, 300, 200, 20)
	cs := LayoutClusters(m, LayoutOpts{})
	if len(cs) != 2 {
		t.Fatalf("want 2 clusters, got %d: %+v", len(cs), cs)
	}
	for _, c := range cs {
		if c.W > 0.7 || c.H > 0.7 {
			t.Errorf("a cluster spans most of the page — the gap was bridged: %+v", c)
		}
	}
}

// A DRAWN FRAME must not bridge everything into one component. Measured on the
// bench's E-size survey: with the border left in, connected components returned
// ONE cluster covering 95% x 94% of the sheet.
func TestAFrameDoesNotBridgeClusters(t *testing.T) {
	m := canvas(800, 800)
	solidLine(m, 10, 10, 790, 10)   // top
	solidLine(m, 10, 780, 790, 780) // bottom
	solidLine(m, 10, 10, 10, 780)   // left
	solidLine(m, 780, 10, 780, 780) // right
	textBlock(m, 60, 60, 250, 150, 20)
	textBlock(m, 480, 520, 250, 150, 20)
	cs := LayoutClusters(m, LayoutOpts{})
	if len(cs) < 2 {
		t.Fatalf("the frame bridged the clusters: got %d", len(cs))
	}
	for _, c := range cs {
		if c.W > 0.8 && c.H > 0.8 {
			t.Errorf("a cluster covers the framed page: %+v", c)
		}
	}
}

// A drawing's LINEWORK connects labels to each other. Erasing long continuous
// runs is what leaves the labels as islands — without it a survey returns one
// blob, which is exactly what it did.
func TestLineworkDoesNotConnectLabels(t *testing.T) {
	m := canvas(800, 800)
	// a boundary running the width, with labels on either side of it
	solidLine(m, 20, 400, 780, 400)
	solidLine(m, 400, 20, 400, 780)
	textBlock(m, 60, 100, 120, 60, 20)
	textBlock(m, 600, 100, 120, 60, 20)
	textBlock(m, 60, 600, 120, 60, 20)
	cs := LayoutClusters(m, LayoutOpts{})
	if len(cs) < 3 {
		t.Fatalf("linework merged the labels: got %d clusters", len(cs))
	}
	for _, c := range cs {
		if c.W > 0.6 && c.H > 0.6 {
			t.Errorf("a cluster spans the drawing: %+v", c)
		}
	}
}

// The lines of ONE paragraph must merge into one block rather than coming back
// as a comb of separate lines — otherwise every line becomes its own region and
// the budget is spent on twenty crops of one paragraph.
func TestParagraphLinesMergeIntoOneBlock(t *testing.T) {
	m := canvas(800, 800)
	textBlock(m, 100, 100, 400, 300, 20)
	cs := LayoutClusters(m, LayoutOpts{})
	if len(cs) != 1 {
		t.Fatalf("a paragraph must be one cluster, got %d", len(cs))
	}
}

// Nothing to find must return nothing, and callers read that as "fall back to
// the geometric grid" — never as "the page is blank".
func TestBlankPageYieldsNoClusters(t *testing.T) {
	if cs := LayoutClusters(canvas(400, 400), LayoutOpts{}); len(cs) != 0 {
		t.Errorf("a blank page has no clusters, got %d", len(cs))
	}
	// Too small to segment is also nothing, not a panic.
	if cs := LayoutClusters(canvas(8, 8), LayoutOpts{}); len(cs) != 0 {
		t.Errorf("a tiny image must yield nothing, got %d", len(cs))
	}
}

// Over the cap, the smallest are MERGED into a neighbour rather than dropped: a
// label read alongside its neighbour is still read, and a dropped one is
// invisible. Same asymmetry the grid's 45% overlap rule is built on.
func TestClusterCapMergesRatherThanDrops(t *testing.T) {
	m := canvas(1200, 1200)
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			textBlock(m, 40+i*190, 40+j*190, 90, 60, 20)
		}
	}
	full := LayoutClusters(m, LayoutOpts{})
	capped := LayoutClusters(m, LayoutOpts{MaxClusters: 5})
	if len(capped) > 5 {
		t.Fatalf("cap not applied: %d", len(capped))
	}
	if len(full) <= len(capped) {
		t.Skip("fixture did not produce more clusters than the cap")
	}
	// Merging must not lose ground: the capped set covers what the full set did.
	var af, ac float64
	for _, c := range full {
		af += c.W * c.H
	}
	for _, c := range capped {
		ac += c.W * c.H
	}
	if ac < af*0.9 {
		t.Errorf("merging lost coverage: %.2f vs %.2f", ac, af)
	}
}
