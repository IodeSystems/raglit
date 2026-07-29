package raglit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"math"
	"sort"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// Reading an image that does not fit in one look.
//
// A vision encoder's token budget is per IMAGE, so a bigger sheet buys nothing.
// Measured on a recorded 27x36.7in land survey: 4011 image tokens for 991 square
// inches, against 3678 for a 94-square-inch letter page — ten times less
// resolution per inch of paper, on the document with the smallest lettering.
//
// What that produced was not a blank or an error. It was tidy prose that had
// dropped the entire legal description and invented plausible auditor file
// numbers. A transcription that READS complete and is not is the failure this
// exists to prevent; see plan/hierarchical-regions.md.
//
// The shape: ask the model what this whole region IS, and which sub-regions
// merit a closer look. Recurse into those. The root says what the sheet is; the
// leaves carry exact text. Both are kept, because a bad region set should
// degrade DETAIL, not coverage.

// Rect is a region of a page in normalized coordinates — 0..1 of the page's
// width and height. Normalized rather than pixels so a region survives being
// re-rendered at a different dpi, which is exactly what a transform does.
type Rect struct {
	X, Y, W, H float64
}

func (r Rect) area() float64 { return r.W * r.H }

// clampToUnit trims a rect to the page. A model asked for boxes in 0..1 will
// occasionally return 1.02; refusing those loses a real region over arithmetic.
func (r Rect) clampToUnit() Rect {
	if r.X < 0 {
		r.W += r.X
		r.X = 0
	}
	if r.Y < 0 {
		r.H += r.Y
		r.Y = 0
	}
	if r.X+r.W > 1 {
		r.W = 1 - r.X
	}
	if r.Y+r.H > 1 {
		r.H = 1 - r.Y
	}
	return r
}

func (r Rect) valid() bool {
	return r.W > 0 && r.H > 0 && r.X >= 0 && r.Y >= 0 && r.X+r.W <= 1.0001 && r.Y+r.H <= 1.0001
}

// within maps a child rect expressed in THIS region's coordinates into page
// coordinates. The model sees a cropped image and answers about that crop, so
// every proposal needs lifting back to the page before it means anything.
func (r Rect) within(child Rect) Rect {
	return Rect{X: r.X + child.X*r.W, Y: r.Y + child.Y*r.H, W: child.W * r.W, H: child.H * r.H}
}

// Region is one node of the read: a piece of a page, at a rotation and scale,
// with what the model said about it.
type Region struct {
	Page     int      `json:"page"`
	BBox     Rect     `json:"bbox"` // page coordinates
	Rotation int      `json:"rotation"`
	Kind     string   `json:"kind,omitempty"` // overview|text-block|table|drawing|legend
	Text     string   `json:"text,omitempty"`
	Flags    []string `json:"flags,omitempty"`
	Depth    int      `json:"depth"`
	// TokensPerSqIn is what this region was actually seen at. Recorded because
	// it is the one quality number available BEFORE any model call, and on its
	// own it condemns a transcription taken at four.
	TokensPerSqIn float64   `json:"tokens_per_sq_in"`
	Children      []*Region `json:"children,omitempty"`
}

// Region flags. Not a score: a confidence number here would be an invented
// statistic. These are conditions that either hold or do not.
const (
	// FlagLowResolution — this region was seen at materially less detail than a
	// letter page gets. Pure arithmetic, available before the model is called.
	FlagLowResolution = "low-resolution"
	// FlagRepetition — the model looped. On a page this fails the document; on a
	// region it is a reason to descend.
	FlagRepetition = "repetition"
	// FlagClipped — ink touches the region boundary, so text may be cut. The
	// first verification tile clipped mid-word ("REPLAT OF BLO"), which is what
	// this measures.
	FlagClipped = "clipped"
	// FlagCycled — descent stopped because it stopped paying: a transform
	// rendered to bytes already seen, or cleared no flag.
	FlagCycled = "cycled"
	// FlagExhausted — the model proposed nothing further. The one POSITIVE
	// signal, and the only one meaning "this is as good as it gets".
	FlagExhausted = "exhausted"
	// FlagBudget — descent stopped because the budget ran out, not because the
	// region was finished. Recorded so a thin read is never mistaken for a
	// complete one.
	FlagBudget = "budget"
)

func (r *Region) addFlag(f string) {
	for _, x := range r.Flags {
		if x == f {
			return
		}
	}
	r.Flags = append(r.Flags, f)
}

func (r *Region) hasFlag(f string) bool {
	for _, x := range r.Flags {
		if x == f {
			return true
		}
	}
	return false
}

// letterTokensPerSqIn is the measured baseline: a 8.5x11 page costs ~3678 image
// tokens, so ~39 tokens per square inch of paper. A region seen at materially
// less than this cannot be read reliably, whatever it returns.
const letterTokensPerSqIn = 39.0

// lowResolutionRatio is how far below the baseline is too far. A half-scale
// region (~20 tokens/sq in) is still usually legible for 10-point text; a
// quarter (~10) is not, and the survey came in at four.
const lowResolutionRatio = 0.5

// maxImageTokens is the CAP a vision encoder puts on one image, and it is the
// whole phenomenon this package exists for.
//
// Measured against a live endpoint: a 1700x2200 letter page costs 3678 tokens
// and a 5401x7345 E-size sheet costs 4011 — 12x the pixels for 9% more tokens.
// The encoder downscales to fit a fixed budget, so a bigger sheet buys nothing
// and the extra area is paid for in lost resolution.
//
// An uncapped patch count would say the survey gets 50,759 tokens and is
// therefore fine, which is exactly backwards.
const maxImageTokens = 4000.0

// tokensForImage estimates what a vision encoder charges for an image.
//
// Approximate on purpose. The exact number is provider-specific and this is used
// only to compare a region against a baseline — a ratio, where a consistent bias
// cancels. ~28x28 pixels per patch, one token per patch, then CAPPED: with the
// cap this predicts 42 tokens/sq in for a letter page against 39 measured, and
// 4.0 for the E-size survey against 4.0 measured.
func tokensForImage(wpx, hpx int) float64 {
	const patch = 28.0
	n := math.Ceil(float64(wpx)/patch) * math.Ceil(float64(hpx)/patch)
	return math.Min(n, maxImageTokens)
}

// resolutionOf reports the tokens per square inch a region gets when rendered at
// dpi, given the page's physical size in inches.
func resolutionOf(bbox Rect, pageWIn, pageHIn float64, dpi int) float64 {
	sqIn := bbox.W * pageWIn * bbox.H * pageHIn
	if sqIn <= 0 {
		return 0
	}
	wpx := int(bbox.W * pageWIn * float64(dpi))
	hpx := int(bbox.H * pageHIn * float64(dpi))
	return tokensForImage(wpx, hpx) / sqIn
}

// isDescent distinguishes narrowing the field of view from re-rendering the same
// region.
//
// The model will propose, as a "sub-region", the region it was just given —
// re-rotated, thresholded, or simply asked for again. Sometimes that is the
// right instruction; as a CHILD it recurses forever on the same pixels. Routing
// is by GEOMETRY, not by what the model called it.
const descentAreaRatio = 0.8

func isDescent(parent, child Rect) bool {
	if parent.area() <= 0 {
		return false
	}
	return child.area()/parent.area() < descentAreaRatio
}

// renderRegion crops a region out of a full-page image and rotates it upright.
//
// Rotation is per REGION, not per page: one 1300x900 cell of the survey holds
// text at four different angles, so a page-level rotation fixes the description
// block and does nothing for the drawing interior.
func renderRegion(pageImg image.Image, bbox Rect, rotation int) ([]byte, error) {
	b := pageImg.Bounds()
	x0 := b.Min.X + int(bbox.X*float64(b.Dx()))
	y0 := b.Min.Y + int(bbox.Y*float64(b.Dy()))
	x1 := x0 + int(bbox.W*float64(b.Dx()))
	y1 := y0 + int(bbox.H*float64(b.Dy()))
	if x1 <= x0 || y1 <= y0 {
		return nil, fmt.Errorf("raglit: region %v is empty at this scale", bbox)
	}
	sub := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	xdraw.Draw(sub, sub.Bounds(), pageImg, image.Pt(x0, y0), xdraw.Src)

	var out image.Image = sub
	switch ((rotation % 360) + 360) % 360 {
	case 90, 180, 270:
		out = rotateImage(sub, ((rotation%360)+360)%360)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rotateImage turns an image by a right angle. Only right angles: an arbitrary
// angle needs resampling and would blur the small lettering this exists to make
// readable, and a text block on a scanned sheet is square to the page far more
// often than not.
func rotateImage(src image.Image, deg int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	var dst *image.RGBA
	switch deg {
	case 90:
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(y, w-1-x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
	case 180:
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(w-1-x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
	case 270:
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
	default:
		return src
	}
	return dst
}

// inkAtEdge reports whether dark pixels touch the region's border, meaning text
// may be cut. Sampled rather than exhaustive: a border row either has ink on it
// or it does not, and one pixel in four is enough to tell.
func inkAtEdge(img image.Image, threshold uint32) bool {
	b := img.Bounds()
	dark := func(x, y int) bool {
		r, g, bl, _ := img.At(x, y).RGBA()
		return (r+g+bl)/3 < threshold
	}
	for x := b.Min.X; x < b.Max.X; x += 4 {
		if dark(x, b.Min.Y) || dark(x, b.Max.Y-1) {
			return true
		}
	}
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		if dark(b.Min.X, y) || dark(b.Max.X-1, y) {
			return true
		}
	}
	return false
}

// imageSHA identifies a rendering by its bytes.
//
// This is also the cycle detector, at no extra cost. A transform that renders to
// bytes already seen IS the cycle: rotating 90 degrees four times returns the
// original SHA, and re-asking for the same bbox at the same dpi and rotation
// returns the original SHA.
func imageSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sortRegions puts a node's children in reading order — top to bottom, then left
// to right — so a rendered tree is stable and diffable.
func sortRegions(rs []*Region) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i].BBox, rs[j].BBox
		if math.Abs(a.Y-b.Y) > 0.02 {
			return a.Y < b.Y
		}
		return a.X < b.X
	})
}

// Flatten walks the tree depth-first, parents before children.
func (r *Region) Flatten() []*Region {
	out := []*Region{r}
	for _, c := range r.Children {
		out = append(out, c.Flatten()...)
	}
	return out
}

// Leaves are the regions with no children — what gets embedded and searched.
// Interior nodes stay searchable through their own overview text, which is why
// a missed region costs detail rather than coverage.
func (r *Region) Leaves() []*Region {
	if len(r.Children) == 0 {
		return []*Region{r}
	}
	var out []*Region
	for _, c := range r.Children {
		out = append(out, c.Leaves()...)
	}
	return out
}

// String renders the tree for a human, one line per region.
func (r *Region) String() string {
	var b strings.Builder
	for _, n := range r.Flatten() {
		fmt.Fprintf(&b, "%s%s %.0fx%.0f%% @%d° %.0ft/in²",
			strings.Repeat("  ", n.Depth), orDashKind(n.Kind),
			n.BBox.W*100, n.BBox.H*100, n.Rotation, n.TokensPerSqIn)
		if len(n.Flags) > 0 {
			fmt.Fprintf(&b, "  [%s]", strings.Join(n.Flags, " "))
		}
		if t := firstLine(n.Text); t != "" {
			if len(t) > 60 {
				t = t[:60] + "…"
			}
			fmt.Fprintf(&b, "  %s", t)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func orDashKind(k string) string {
	if k == "" {
		return "region"
	}
	return k
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
