package raglit

import (
	"bytes"
	"image"
	"sort"
)

// LineSpan measures how far a crop's text lines RUN ACROSS it: the median
// fraction of the width covered by an inked row, over the rows that hold ink.
//
// It exists to stop tiling from cutting prose. gridFor picks columns and rows
// from the PAGE's aspect, so a near-square handwritten letter gets a 3x3 grid
// and every full-width line is severed twice. Measured on olmOCR-bench
// old_scans/1.pdf: "Comrade" came back as "rade", "Sickles" as "ckles", "Dear
// Sir" as "er Sir" — 2014 characters holding 60% of the expected words, against
// 954 characters holding 100% from one plain read of the same page.
//
// A drawing does not have this problem: its labels are islands, and a cut
// between them severs nothing. So the number that decides is not "is this a
// drawing" — which a model answers wrongly at low resolution, and which this
// package already tried and removed — but how wide the ink actually runs.
//
// Returns 0 when there is nothing to measure, which callers must read as
// "unknown" and not as "no lines".
func LineSpan(img image.Image) float64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 16 || h < 16 {
		return 0
	}
	// One pass for the ink threshold: Otsu is overkill here because the decision
	// is coarse, and a mid-grey cut is stable on both a faded fax and a clean
	// scan. Sampling rows keeps this cheap on a 15 megapixel crop.
	const step = 2
	var spans []float64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		first, last, ink := -1, -1, 0
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if (r+g+bl)/3 < 0x8000 { // darker than mid-grey
				if first < 0 {
					first = x
				}
				last = x
				ink++
			}
		}
		// A row is a TEXT row only if it holds a little ink and not a lot: a
		// horizontal rule, a table border or a black margin would otherwise read
		// as the widest line on the page and force every crop to look like prose.
		frac := float64(ink) / float64(w)
		if first < 0 || frac < 0.01 || frac > 0.60 {
			continue
		}
		spans = append(spans, float64(last-first)/float64(w))
	}
	if len(spans) < 4 {
		return 0
	}
	sort.Float64s(spans)
	return spans[len(spans)/2]
}

// LineSpanPNG measures encoded bytes — the form a region's crop already has.
func LineSpanPNG(data []byte) float64 {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0
	}
	return LineSpan(img)
}

// proseLineSpan is where a crop is treated as running text rather than scattered
// labels: lines covering more than 60% of the width.
//
// Chosen above a survey's longest label runs and below prose. It only ever
// removes columns from the grid, so an over-eager reading costs some tile
// squareness; an under-eager one costs severed words, which is the failure that
// motivated the measurement.
const proseLineSpan = 0.60
