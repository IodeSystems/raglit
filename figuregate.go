package raglit

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "golang.org/x/image/tiff"
)

// The raster figure gate (§3a, scanned half): a page image is "figure-like"
// when its ink is both plentiful and spatially concentrated — the signature
// of a diagram, chart or photo on the page, as opposed to text.
//
// Measured rationale: a full text page at 200 DPI carries roughly 5-15% ink
// spread in regular line rhythm; a page holding a large diagram exceeds that
// floor and concentrates well over a third of its ink in a run of rows. The
// thresholds sit on the conservative side on purpose — a false positive pays
// one vision call (and moves the page to llm-seg), a false negative loses the
// figure silently.
const (
	// figureGateGrid is the downsample grid. 64x64 keeps the decode cheap and
	// is fine for a page-level decision.
	figureGateGrid = 64
	// figureGateDark is the luminance (0-1) at or below which a pixel is ink.
	figureGateDark = 0.5
	// figureGateInkFloor: below this total ink fraction there is no figure to
	// describe — the page is text (or blank).
	figureGateInkFloor = 0.15
	// figureGateInkCeiling: above this the page is mostly dark (a photo, a
	// black header, a scanner artifact), not a diagram on paper — the VLM's
	// figure description of that is not worth paying for on every such page.
	figureGateInkCeiling = 0.7
	// figureGateBlockRows is the minimum run of consecutive rows that must
	// carry elevated ink. Text lines are single rows of the grid; a run this
	// long is a block, not a line.
	figureGateBlockRows = 8
	// figureGateRowInk is the per-row ink fraction that counts a row as
	// elevated. A full text line at 64px width carries well under this.
	figureGateRowInk = 0.5
	// figureGateBlockInkFraction: the elevated run must hold at least this
	// fraction of the page's total ink — the concentration test. A diagram's
	// block holds most of the page's ink; scattered dark lines do not.
	figureGateBlockInkFraction = 0.35
)

// figureLikeImage reports whether a page image looks like it carries a figure:
// a large, concentrated block of ink rather than regular text. Conservative by
// design — it is a gate on a costly call, not a classifier. A page it does not
// flag is handled exactly as before; the gate only ADDS escalation.
//
// Returns the flag, the measured ink fraction (0-1) and the length in grid rows
// of the longest concentrated block, for the trace line.
func figureLikeImage(data []byte) (bool, float64, int) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if src == nil || err != nil {
		// Not decodable here (mime, corruption) — treat as not figure-like;
		// the cascade behaves exactly as it does today for such a page.
		return false, 0, 0
	}
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()

	// Downsample to the 64x64 grid by sampling the source at each cell's
	// centre. This avoids the xdraw scaling pipeline (which composites
	// through its internal colour model and can produce an all-dark grid
	// for single-channel sources) and is good enough for a page-level gate.
	rowInk := make([]int, figureGateGrid)
	ink := 0
	for gy := 0; gy < figureGateGrid; gy++ {
		sy := sb.Min.Y + (gy+1)*sh/figureGateGrid/2
		for gx := 0; gx < figureGateGrid; gx++ {
			sx := sb.Min.X + (gx+1)*sw/figureGateGrid/2
			r, g, b, _ := src.At(sx, sy).RGBA()
			// RGBA() returns 16-bit values; luminance in 0-1.
			lum := (float64(r)*299 + float64(g)*587 + float64(b)*114) / 1000 / 65535
			if lum <= figureGateDark {
				ink++
				rowInk[gy]++
			}
		}
	}
	inkFrac := float64(ink) / float64(figureGateGrid*figureGateGrid)
	if inkFrac < figureGateInkFloor || inkFrac > figureGateInkCeiling {
		return false, inkFrac, 0
	}
	// Longest run of consecutive elevated rows, and the ink inside it.
	bestRun, bestInk := 0, 0
	run, runInk := 0, 0
	for _, r := range rowInk {
		if float64(r)/float64(figureGateGrid) >= figureGateRowInk {
			run++
			runInk += r
			if run > bestRun {
				bestRun, bestInk = run, runInk
			}
		} else {
			run, runInk = 0, 0
		}
	}
	if bestRun < figureGateBlockRows {
		return false, inkFrac, bestRun
	}
	if float64(bestInk)/float64(ink) < figureGateBlockInkFraction {
		return false, inkFrac, bestRun
	}
	return true, inkFrac, bestRun
}
