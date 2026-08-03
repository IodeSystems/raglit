package raglit

import (
	"image"
	"image/color"
	"math"
	"sort"
)

// Repairing a region rather than improving it.
//
// Measured 2026-08-03 (plan/hierarchical-regions.md): a filter applied to an
// undamaged render buys nothing and sometimes costs a fact, while on a damaged
// one it is the difference between reading a page and reading nothing. A faded,
// blurred crop of the survey's monument table read 0 of 5 facts unfiltered and 3
// of 5 with contrast or sharpening.
//
// So filters are not a quality knob. They are a repair, applied to damage that
// was MEASURED first — which is why the damage metrics live here beside them.

// RegionFilter is a repair applied to a region's crop before it is read. A
// closed set on purpose: every one of these was measured on a page, and the
// measurements do not transfer to a filter nobody ran.
type RegionFilter string

const (
	// FilterNone is the crop as rendered.
	FilterNone RegionFilter = ""
	// FilterContrast is CLAHE at the setting that was measured to help — clip 2.0
	// over an 8x8 tile grid. The STRONGER setting (clip 4.0, 16x16) recovered
	// nothing on the same page, so the parameters are not a detail.
	FilterContrast RegionFilter = "contrast"
	// FilterSharpen is a mild unsharp mask, sigma 1.0 amount 0.4. Also a measured
	// setting: at sigma 2.0 amount 0.8 the same page went back to its unfiltered
	// score.
	FilterSharpen RegionFilter = "sharpen"
)

// ValidRegionFilter reports whether a name is one this package can apply. A
// proposal naming anything else is refused rather than silently ignored, so a
// model inventing "denoise" is visible instead of quietly doing nothing.
func ValidRegionFilter(f RegionFilter) bool {
	switch f {
	case FilterNone, FilterContrast, FilterSharpen:
		return true
	}
	return false
}

// Damage thresholds.
//
// Measured over the bench fixtures and a deliberately degraded crop:
//
//	clean crop                       lapvar 6593   range 255
//	same crop, 4deg skew + blur 1.6  lapvar   51   range 118
//	ocr-survey-facts                 lapvar 5135   range 255
//	ocr-survey-corners               lapvar 2967   range 255
//	ocr-scanned-exhibit              lapvar 2562   range 243
//	ocr-drawing-dimensions (a fax)   lapvar  844   range 232
//
// Two orders of magnitude between damaged and clean, with every real page well
// clear of the gap and the faxed sheet correctly the lowest of them. The
// thresholds sit in the empty middle rather than being tuned to either end.
//
// PROVISIONAL: laplacian variance scales with how much ink is on a crop, so a
// sparse region of a clean page can score low without being blurred. The flag is
// a reason to TRY a repair, and the repair still has to clear the flag to be
// kept — which is what stops a bad threshold from costing anything but a call.
const (
	blurredLapVar   = 300.0
	fadedRangeSpan  = 160
	damageMinPixels = 64 * 64 // below this the statistics are noise
)

// toGray converts to 8-bit grayscale. The filters and the damage metrics both
// work on luminance: a document is ink on paper, and the colour channels carry
// nothing this needs.
func toGray(img image.Image) *image.Gray {
	if g, ok := img.(*image.Gray); ok {
		return g
	}
	b := img.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.SetGray(x, y, color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray))
		}
	}
	return out
}

// LaplacianVariance is the variance of the 4-neighbour Laplacian: the standard
// cheap focus measure. Sharp edges produce large second derivatives, blur
// flattens them, so a low number means the strokes have been smeared.
func LaplacianVariance(img image.Image) float64 {
	g := toGray(img)
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return 0
	}
	var sum, sumSq float64
	n := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			c := float64(g.GrayAt(x, y).Y)
			v := float64(g.GrayAt(x-1, y).Y) + float64(g.GrayAt(x+1, y).Y) +
				float64(g.GrayAt(x, y-1).Y) + float64(g.GrayAt(x, y+1).Y) - 4*c
			sum += v
			sumSq += v * v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / float64(n)
	return sumSq/float64(n) - mean*mean
}

// DynamicRange is the 1st-to-99th percentile spread of luminance. Percentiles
// rather than min/max because one black speck and one white pixel would report a
// full range on a page that is entirely mid-grey.
func DynamicRange(img image.Image) int {
	g := toGray(img)
	b := g.Bounds()
	var hist [256]int
	n := 0
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			hist[g.GrayAt(x, y).Y]++
			n++
		}
	}
	if n == 0 {
		return 0
	}
	pct := func(p float64) int {
		want := int(p * float64(n))
		acc := 0
		for v := 0; v < 256; v++ {
			acc += hist[v]
			if acc >= want {
				return v
			}
		}
		return 255
	}
	return pct(0.99) - pct(0.01)
}

// DamageOf reports what is measurably wrong with a crop's PIXELS, as flags.
//
// Taken on the full-resolution crop, which is the point: the model is shown a
// downsampled version, and at that scale it cannot tell a blurred page from a
// small picture of a sharp one. Measured — asked to diagnose a crop blurred at
// sigma 1.6, the vision model answered "skew" and "noise" with 0.9 confidence
// and prescribed a deskew, which is the one repair that makes a blurred region
// WORSE. This function is what it could not see.
func DamageOf(img image.Image) []string {
	b := img.Bounds()
	if b.Dx()*b.Dy() < damageMinPixels {
		return nil
	}
	var flags []string
	if LaplacianVariance(img) < blurredLapVar {
		flags = append(flags, FlagBlurred)
	}
	if DynamicRange(img) < fadedRangeSpan {
		flags = append(flags, FlagFaded)
	}
	return flags
}

// ApplyRegionFilter runs one repair over a crop. An unknown filter returns the
// image unchanged — the caller validates; this does not silently invent one.
func ApplyRegionFilter(img image.Image, f RegionFilter) image.Image {
	switch f {
	case FilterContrast:
		return clahe(toGray(img), 2.0, 8)
	case FilterSharpen:
		return unsharpMask(toGray(img), 1.0, 0.4)
	default:
		return img
	}
}

// gaussianBlurGray blurs separably. Radius is 3 sigma, which is where the kernel
// has nothing left worth adding.
func gaussianBlurGray(src *image.Gray, sigma float64) *image.Gray {
	if sigma <= 0 {
		return src
	}
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	var sum float64
	for i := -r; i <= r; i++ {
		v := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		k[i+r] = v
		sum += v
	}
	for i := range k {
		k[i] /= sum
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	tmp := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -r; i <= r; i++ {
				acc += k[i+r] * float64(src.GrayAt(clamp(x+i, 0, w-1), y).Y)
			}
			tmp.SetGray(x, y, color.Gray{Y: uint8(clamp(int(acc+0.5), 0, 255))})
		}
	}
	out := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -r; i <= r; i++ {
				acc += k[i+r] * float64(tmp.GrayAt(x, clamp(y+i, 0, h-1)).Y)
			}
			out.SetGray(x, y, color.Gray{Y: uint8(clamp(int(acc+0.5), 0, 255))})
		}
	}
	return out
}

// unsharpMask is src + amount*(src - blur(src)) — the practical approximation to
// deconvolving a gaussian, which is what "sharpen" means when the blur is not
// known exactly.
func unsharpMask(src *image.Gray, sigma, amount float64) *image.Gray {
	blur := gaussianBlurGray(src, sigma)
	b := src.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			s := float64(src.GrayAt(x, y).Y)
			v := s + amount*(s-float64(blur.GrayAt(x, y).Y))
			switch {
			case v < 0:
				v = 0
			case v > 255:
				v = 255
			}
			out.SetGray(x, y, color.Gray{Y: uint8(v + 0.5)})
		}
	}
	return out
}

// clahe is contrast-limited adaptive histogram equalisation: equalise within
// each tile, clip the histogram so noise in a flat tile is not amplified into
// texture, and interpolate between neighbouring tiles' mappings so the tile grid
// does not show up as blocking.
//
// Adaptive rather than global because the damage it repairs is uneven — a scan
// lit from one side, a fax that faded across the sheet. A global stretch was
// measured and recovered nothing.
func clahe(src *image.Gray, clipLimit float64, tiles int) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < tiles || h < tiles {
		return src
	}
	tw, th := w/tiles, h/tiles
	if tw < 2 || th < 2 {
		return src
	}
	// One mapping per tile.
	maps := make([][256]uint8, tiles*tiles)
	for ty := 0; ty < tiles; ty++ {
		for tx := 0; tx < tiles; tx++ {
			x0, y0 := tx*tw, ty*th
			x1, y1 := x0+tw, y0+th
			if tx == tiles-1 {
				x1 = w
			}
			if ty == tiles-1 {
				y1 = h
			}
			var hist [256]int
			n := 0
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					hist[src.GrayAt(x, y).Y]++
					n++
				}
			}
			if n == 0 {
				continue
			}
			// Clip, and redistribute what was cut evenly — the "limited" half.
			limit := int(clipLimit * float64(n) / 256.0)
			if limit < 1 {
				limit = 1
			}
			excess := 0
			for v := 0; v < 256; v++ {
				if hist[v] > limit {
					excess += hist[v] - limit
					hist[v] = limit
				}
			}
			give := excess / 256
			for v := 0; v < 256; v++ {
				hist[v] += give
			}
			cum, scale := 0, 255.0/float64(n)
			for v := 0; v < 256; v++ {
				cum += hist[v]
				out := int(float64(cum)*scale + 0.5)
				if out > 255 {
					out = 255
				}
				maps[ty*tiles+tx][v] = uint8(out)
			}
		}
	}
	// Bilinear interpolation between the four surrounding tile centres.
	out := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		fy := (float64(y) - float64(th)/2) / float64(th)
		ty0 := int(math.Floor(fy))
		wy := fy - float64(ty0)
		for x := 0; x < w; x++ {
			fx := (float64(x) - float64(tw)/2) / float64(tw)
			tx0 := int(math.Floor(fx))
			wx := fx - float64(tx0)
			v := src.GrayAt(x, y).Y
			at := func(tx, ty int) float64 {
				if tx < 0 {
					tx = 0
				}
				if ty < 0 {
					ty = 0
				}
				if tx > tiles-1 {
					tx = tiles - 1
				}
				if ty > tiles-1 {
					ty = tiles - 1
				}
				return float64(maps[ty*tiles+tx][v])
			}
			top := at(tx0, ty0)*(1-wx) + at(tx0+1, ty0)*wx
			bot := at(tx0, ty0+1)*(1-wx) + at(tx0+1, ty0+1)*wx
			val := top*(1-wy) + bot*wy
			if val < 0 {
				val = 0
			}
			if val > 255 {
				val = 255
			}
			out.SetGray(x, y, color.Gray{Y: uint8(val + 0.5)})
		}
	}
	return out
}

// sortedFilters gives a stable order for a set of proposed filters, so a tree is
// diffable across reads.
func sortedFilters(fs []RegionFilter) []RegionFilter {
	out := append([]RegionFilter(nil), fs...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
