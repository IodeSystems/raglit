package raglit

import (
	"bytes"
	"image"
	"math"
	"sort"
)

// Finding the content's own boundaries, so a cut never lands inside it.
//
// tileRegion cuts a blind geometric grid: gridFor picks columns and rows from
// the PAGE's aspect so each tile comes out square. On a drawing that is fine —
// labels are islands and a seam between them severs nothing. On running text it
// is destructive: a 3x3 grid over a handwritten letter severed every full-width
// line twice, turning "Comrade" into "rade" and "Sickles" into "ckles", and the
// 45% overlap rule cannot help because a model cannot tell that a half-visible
// word is half-visible.
//
// Two attempts to fix that by CLASSIFYING the page both failed, and their
// failures are what shaped this:
//
//   - ink EXTENT (leftmost to rightmost) is defeated by a drawn BORDER: the
//     bench's E-size survey measures 0.94 of the width per row while only 8% of
//     that span holds ink — two frame marks with a drawing between them.
//   - longest contiguous RUN is defeated by drawn LINEWORK: a survey's boundary
//     lines and roads are long unbroken ink, so the drawing scored HIGHER than
//     the prose at every gap tolerance tried.
//
// So this does not ask what the page IS. It finds where the ink CLUSTERS, and
// the gaps between clusters are where cutting is safe by construction. That is
// run-length smoothing followed by connected components — the classical layout
// analysis, and measurement rather than a model call, which this package has now
// been taught three times.

// LayoutOpts tunes cluster finding. Zero values are sensible for a 200 DPI scan.
type LayoutOpts struct {
	// CellPx is the working grid's resolution. Segmentation does not need full
	// resolution and a 40 megapixel sheet does need to not be scanned per pixel.
	CellPx int // 0 → 8
	// GapCells is how far apart ink may be and still belong to one cluster,
	// measured in cells. It is the single knob that decides whether the lines of
	// a paragraph merge into one block (they should) or a drawing's separate
	// labels merge into one (they should not).
	GapCells int // 0 → 3
	// MinCells drops specks: a cluster smaller than this is noise, not content.
	MinCells int // 0 → 6
	// MaxClusters caps the result. Above it the smallest are merged into their
	// nearest neighbour rather than dropped — losing a label is worse than
	// reading it alongside its neighbour.
	MaxClusters int // 0 → 24
}

func (o LayoutOpts) resolved() LayoutOpts {
	if o.CellPx <= 0 {
		o.CellPx = 8
	}
	if o.GapCells <= 0 {
		o.GapCells = 3
	}
	if o.MinCells <= 0 {
		o.MinCells = 6
	}
	if o.MaxClusters <= 0 {
		o.MaxClusters = 24
	}
	return o
}

// LayoutClusters returns the content clusters of an image, as fractions of it.
//
// Empty when there is nothing to find, which callers must treat as "fall back to
// the geometric grid" rather than as "the page is blank".
func LayoutClusters(img image.Image, o LayoutOpts) []Rect {
	o = o.resolved()
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	cw, ch := W/o.CellPx, H/o.CellPx
	if cw < 4 || ch < 4 {
		return nil
	}
	ink := inkGrid(img, o.CellPx, cw, ch)
	dropLinework(ink, cw, ch)
	comps := components(dilate(ink, cw, ch, o.GapCells), cw, ch)

	var out []Rect
	for _, c := range comps {
		if c.cells < o.MinCells {
			continue
		}
		out = append(out, Rect{
			X: float64(c.x0) / float64(cw),
			Y: float64(c.y0) / float64(ch),
			W: float64(c.x1-c.x0+1) / float64(cw),
			H: float64(c.y1-c.y0+1) / float64(ch),
		})
	}
	// Largest first: a caller with a budget should spend it on the biggest
	// clusters, and a caller merging to a cap merges the tail.
	sort.Slice(out, func(i, j int) bool { return out[i].area() > out[j].area() })
	if len(out) > o.MaxClusters {
		out = mergeTail(out, o.MaxClusters)
	}
	return out
}

// LayoutClustersPNG segments encoded bytes — the form a region's crop has.
func LayoutClustersPNG(data []byte, o LayoutOpts) []Rect {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return LayoutClusters(img, o)
}

// inkGrid reduces the image to a coarse boolean grid. A cell is inked when ANY
// sampled pixel in it is darker than mid-grey — any, not a majority, because a
// 6pt label is a few dark pixels in an otherwise white cell and losing it here
// loses it everywhere downstream.
func inkGrid(img image.Image, cell, cw, ch int) []bool {
	b := img.Bounds()
	g := make([]bool, cw*ch)
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			for y := 0; y < cell && !g[cy*cw+cx]; y += 2 {
				for x := 0; x < cell; x += 2 {
					px, py := b.Min.X+cx*cell+x, b.Min.Y+cy*cell+y
					if px >= b.Max.X || py >= b.Max.Y {
						continue
					}
					r, gg, bl, _ := img.At(px, py).RGBA()
					if (r+gg+bl)/3 < 0x8000 {
						g[cy*cw+cx] = true
						break
					}
				}
			}
		}
	}
	return g
}

// dropLinework clears drawn RULES AND LINES so they cannot bridge every cluster
// into one.
//
// Generalised from clearing only the border, because the border was never the
// whole problem: on the bench's E-size survey, connected components over the raw
// ink returned ONE cluster covering 95% x 94% of the sheet. A survey's boundary
// lines, roads and lot edges connect every label to every other, so clustering
// all ink clusters the drawing rather than its labels.
//
// The discriminator is continuity, measured before any dilation: a drawn line is
// an unbroken run of cells, and a line of TEXT is not — it breaks at every word.
// So a run longer than lineworkRun is structure and is erased, and what survives
// is the text.
//
// The old edge-only frame pass is subsumed: a border is just the longest run of
// all.
func dropLinework(g []bool, cw, ch int) {
	// A run this long is a drawn line. Below it, a dense word or a table cell's
	// contents survive — deliberately generous, because erasing text here loses
	// it from every later stage and there is no way to get it back.
	runX, runY := max(4, cw/8), max(4, ch/8)
	for cy := 0; cy < ch; cy++ {
		start := -1
		for cx := 0; cx <= cw; cx++ {
			on := cx < cw && g[cy*cw+cx]
			if on && start < 0 {
				start = cx
			}
			if !on && start >= 0 {
				if cx-start >= runX {
					for x := start; x < cx; x++ {
						g[cy*cw+x] = false
					}
				}
				start = -1
			}
		}
	}
	for cx := 0; cx < cw; cx++ {
		start := -1
		for cy := 0; cy <= ch; cy++ {
			on := cy < ch && g[cy*cw+cx]
			if on && start < 0 {
				start = cy
			}
			if !on && start >= 0 {
				if cy-start >= runY {
					for y := start; y < cy; y++ {
						g[y*cw+cx] = false
					}
				}
				start = -1
			}
		}
	}
}

// dropFrame clears a drawn border so it cannot bridge every cluster into one.
//
// This is the failure that defeated both earlier attempts: a survey's frame puts
// ink in every row and column, so projection valleys found ZERO interior gutters
// on the bench's E-size sheet, and any dilation would connect the whole page
// through the border. A frame is recognisable without knowing it is a frame —
// a near-continuous inked line along a row or column close to an edge — so it is
// cleared by that description rather than by detecting "a border".
func dropFrame(g []bool, cw, ch int) {
	const edge = 0.06  // how far in from an edge a frame may sit
	const solid = 0.90 // how much of the span must be inked to be a rule
	ex, ey := max(1, int(float64(cw)*edge)), max(1, int(float64(ch)*edge))
	for cy := 0; cy < ch; cy++ {
		if cy > ey && cy < ch-ey-1 {
			continue
		}
		n := 0
		for cx := 0; cx < cw; cx++ {
			if g[cy*cw+cx] {
				n++
			}
		}
		if float64(n) >= solid*float64(cw) {
			for cx := 0; cx < cw; cx++ {
				g[cy*cw+cx] = false
			}
		}
	}
	for cx := 0; cx < cw; cx++ {
		if cx > ex && cx < cw-ex-1 {
			continue
		}
		n := 0
		for cy := 0; cy < ch; cy++ {
			if g[cy*cw+cx] {
				n++
			}
		}
		if float64(n) >= solid*float64(ch) {
			for cy := 0; cy < ch; cy++ {
				g[cy*cw+cx] = false
			}
		}
	}
}

// dilate grows ink by gap cells in both axes, so a paragraph's lines join into
// one block while a drawing's separate labels stay separate. Run-length
// smoothing, in the classical sense.
func dilate(g []bool, cw, ch, gap int) []bool {
	out := make([]bool, len(g))
	copy(out, g)
	// horizontal: bridge word and character gaps
	for cy := 0; cy < ch; cy++ {
		last := -1
		for cx := 0; cx < cw; cx++ {
			if g[cy*cw+cx] {
				if last >= 0 && cx-last <= gap {
					for x := last; x < cx; x++ {
						out[cy*cw+x] = true
					}
				}
				last = cx
			}
		}
	}
	// vertical: bridge line spacing, on the horizontally-smoothed result so a
	// paragraph closes into a block rather than a comb of separate lines
	src := make([]bool, len(out))
	copy(src, out)
	for cx := 0; cx < cw; cx++ {
		last := -1
		for cy := 0; cy < ch; cy++ {
			if src[cy*cw+cx] {
				if last >= 0 && cy-last <= gap {
					for y := last; y < cy; y++ {
						out[y*cw+cx] = true
					}
				}
				last = cy
			}
		}
	}
	return out
}

type comp struct {
	x0, y0, x1, y1 int
	cells          int
}

// components labels 4-connected regions of the dilated grid, iteratively so a
// tall page cannot overflow a recursive stack.
func components(g []bool, cw, ch int) []comp {
	seen := make([]bool, len(g))
	var out []comp
	stack := make([]int, 0, 256)
	for i := range g {
		if !g[i] || seen[i] {
			continue
		}
		c := comp{x0: cw, y0: ch}
		stack = append(stack[:0], i)
		seen[i] = true
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%cw, p/cw
			c.cells++
			c.x0, c.y0 = min(c.x0, x), min(c.y0, y)
			c.x1, c.y1 = max(c.x1, x), max(c.y1, y)
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= cw || ny >= ch {
					continue
				}
				q := ny*cw + nx
				if g[q] && !seen[q] {
					seen[q] = true
					stack = append(stack, q)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// mergeTail folds the smallest clusters into the nearest larger one until the
// count fits. Merging rather than dropping: a label read alongside its neighbour
// is still read, and a dropped one is invisible — the same asymmetry the grid's
// 45% overlap rule is built on.
func mergeTail(rs []Rect, cap int) []Rect {
	for len(rs) > cap {
		small := rs[len(rs)-1]
		rs = rs[:len(rs)-1]
		best, bd := 0, 1e9
		for i, r := range rs {
			dx := (r.X + r.W/2) - (small.X + small.W/2)
			dy := (r.Y + r.H/2) - (small.Y + small.H/2)
			if d := dx*dx + dy*dy; d < bd {
				best, bd = i, d
			}
		}
		rs[best] = union(rs[best], small)
	}
	return rs
}

// union is the bounding box of two rects. Written with math.Min/Max rather than
// the builtins because this package defines int-only min/max of its own, which
// shadow them.
func union(a, b Rect) Rect {
	x0, y0 := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	x1, y1 := math.Max(a.X+a.W, b.X+b.W), math.Max(a.Y+a.H, b.Y+b.H)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}
