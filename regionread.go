package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"regexp"
	"strings"
)

// RegionReader walks a page as a tree of regions.
//
// The model is asked, at every node, for two things: what this whole region IS,
// and which sub-regions merit a closer look. Descent recurses into the second
// while keeping the first, which is what makes a bad region set cost DETAIL
// rather than coverage.
type RegionReader struct {
	// Ask returns the model's reading of one rendered region. Separated from the
	// walk so the descent rules — budgets, routing, cycle detection — are
	// testable without a model, which is most of what can go wrong here.
	Ask func(ctx context.Context, img PageImage, depth int) (RegionReading, error)

	// PageWIn, PageHIn are the sheet's physical size. The whole reason this
	// exists is that a bigger sheet buys no extra tokens, so the physical size
	// is the input that decides everything.
	PageWIn, PageHIn float64
	DPI              int

	MaxDepth      int // 0 → 3
	MaxChildren   int // per node, 0 → 8
	MaxTransforms int // per region, 0 → 2
	MaxCalls      int // whole document, 0 → 40
	MinRegionIn   float64

	calls int
	seen  map[string]bool // image SHA → already read; this IS the cycle detector
}

// RegionReading is what the model returns for one region.
type RegionReading struct {
	// Description covers everything visible at this scale — a transcription for
	// a text block, a summary for a drawing.
	Description string `json:"description"`
	Kind        string `json:"kind"`
	// Regions are proposals in THIS region's coordinates, 0..1.
	Regions []RegionProposal `json:"regions"`
	// Repeated is set when the generation looped. On a page that fails the
	// document; on a region it is a reason to descend.
	Repeated bool `json:"-"`
}

// RegionProposal is one sub-region the model thinks is worth a closer look.
type RegionProposal struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Rotation int     `json:"rotation"`
	Reason   string  `json:"reason"`
	Kind     string  `json:"kind"`
}

func (p RegionProposal) rect() Rect { return Rect{X: p.X, Y: p.Y, W: p.W, H: p.H} }

func (rr *RegionReader) defaults() {
	if rr.MaxDepth == 0 {
		rr.MaxDepth = 3
	}
	if rr.MaxChildren == 0 {
		rr.MaxChildren = 8
	}
	if rr.MaxTransforms == 0 {
		rr.MaxTransforms = 2
	}
	if rr.MaxCalls == 0 {
		rr.MaxCalls = 40
	}
	if rr.MinRegionIn == 0 {
		rr.MinRegionIn = 1.0 // an inch square; below this another descent buys nothing
	}
	if rr.DPI == 0 {
		rr.DPI = 200
	}
	if rr.seen == nil {
		rr.seen = map[string]bool{}
	}
}

// Read walks the page and returns the root region.
func (rr *RegionReader) Read(ctx context.Context, page image.Image, pageNo int) (*Region, error) {
	rr.defaults()
	root := &Region{Page: pageNo, BBox: Rect{0, 0, 1, 1}, Depth: 0}
	if err := rr.visit(ctx, page, root, 0); err != nil {
		return root, err
	}
	return root, nil
}

// visit reads one region and descends into what it proposes.
func (rr *RegionReader) visit(ctx context.Context, page image.Image, reg *Region, transformsUsed int) error {
	reg.TokensPerSqIn = resolutionOf(reg.BBox, rr.PageWIn, rr.PageHIn, rr.DPI)
	if reg.TokensPerSqIn < letterTokensPerSqIn*lowResolutionRatio {
		// Known BEFORE the model is called, and on its own enough to distrust
		// whatever comes back.
		reg.addFlag(FlagLowResolution)
	}

	img, err := renderRegion(page, reg.BBox, reg.Rotation)
	if err != nil {
		return err
	}
	sha := imageSHA(img)
	if rr.seen[sha] {
		// This exact rendering has been read already. Refusing here is what stops
		// the model's "re-rotate/filter the same section" proposal from
		// recursing forever on the same pixels.
		reg.addFlag(FlagCycled)
		return nil
	}
	if rr.calls >= rr.MaxCalls {
		reg.addFlag(FlagBudget)
		return nil
	}
	rr.seen[sha] = true
	rr.calls++

	reading, err := rr.Ask(ctx, PageImage{Page: reg.Page, Mime: "image/png", Data: img}, reg.Depth)
	if err != nil {
		return err
	}
	reg.Text = reading.Description
	if reg.Kind == "" {
		reg.Kind = reading.Kind
	}
	if reading.Repeated {
		reg.addFlag(FlagRepetition)
	}

	if reg.Depth >= rr.MaxDepth {
		reg.addFlag(FlagBudget)
		return nil
	}

	descents, transforms := rr.route(reg, reading.Regions)
	if len(descents) == 0 && len(transforms) == 0 {
		reg.addFlag(FlagExhausted)
		return nil
	}

	// A transform re-renders the SAME region and must earn its place: it is
	// justified by a flag it means to clear, and if it clears nothing the branch
	// stops. Applied to THIS region rather than as a child, because it is not one.
	for _, t := range transforms {
		if transformsUsed >= rr.MaxTransforms {
			reg.addFlag(FlagCycled)
			break
		}
		alt := &Region{Page: reg.Page, BBox: reg.BBox, Rotation: t.Rotation,
			Kind: reg.Kind, Depth: reg.Depth}
		if err := rr.visit(ctx, page, alt, transformsUsed+1); err != nil {
			return err
		}
		if alt.hasFlag(FlagCycled) || !transformHelped(reg, alt) {
			// Bought nothing. Two of these in a row end the region as a flagged
			// leaf, which is the honest outcome for six-point text on a scan.
			reg.addFlag(FlagCycled)
			break
		}
		// It helped: keep the better reading and its flags.
		reg.Text, reg.Flags, reg.Rotation = alt.Text, alt.Flags, alt.Rotation
		reg.Children = append(reg.Children, alt.Children...)
	}

	for _, d := range descents {
		if rr.calls >= rr.MaxCalls {
			reg.addFlag(FlagBudget)
			break
		}
		child := &Region{Page: reg.Page, BBox: d.bbox, Rotation: d.rotation,
			Kind: d.kind, Depth: reg.Depth + 1}
		if err := rr.visit(ctx, page, child, 0); err != nil {
			return err
		}
		reg.Children = append(reg.Children, child)
	}
	sortRegions(reg.Children)
	return nil
}

type descent struct {
	bbox     Rect
	rotation int
	kind     string
}

// route splits the model's proposals into descents and transforms BY GEOMETRY.
//
// A child covering most of its parent is a transform wearing a descent's
// clothes, whatever the model called it. Anything neither smaller nor
// differently rendered is refused outright — it is the model asking to be shown
// the same thing again.
func (rr *RegionReader) route(parent *Region, props []RegionProposal) ([]descent, []RegionProposal) {
	var ds []descent
	var ts []RegionProposal
	for _, p := range props {
		r := p.rect().clampToUnit()
		if !r.valid() {
			continue
		}
		page := parent.BBox.within(r)
		if wIn, hIn := page.W*rr.PageWIn, page.H*rr.PageHIn; wIn < rr.MinRegionIn && hIn < rr.MinRegionIn {
			// Below this another descent buys no resolution; the pixels are
			// already all there.
			continue
		}
		if isDescent(Rect{0, 0, 1, 1}, r) {
			padded := parent.BBox.within(r.padded(descentPad))
			// Duplicate proposals are common — the model names the same block
			// twice under different reasons — and each one costs a model call.
			if dup := indexOfOverlap(ds, padded, 0.6); dup >= 0 {
				continue
			}
			ds = append(ds, descent{bbox: padded, rotation: p.Rotation, kind: p.Kind})
			continue
		}
		if p.Rotation != parent.Rotation {
			ts = append(ts, p)
		}
		// Same area AND same rotation: nothing new was asked for. Dropped.
	}
	if len(ds) > rr.MaxChildren {
		ds = ds[:rr.MaxChildren]
	}
	return ds, ts
}

// transformHelped decides whether a re-render earned its place.
//
// Two signals, because one is not enough. Clearing a flag is the strong one —
// a narrower field breaking a repetition loop, a threshold resolving a
// disagreement.
//
// But a ROTATION cannot clear `low-resolution`: the area is unchanged, so the
// arithmetic is identical. Judging rotation on flags alone rejected every
// rotation, including the one that recovered the survey's legal description
// from a sideways text block. So more TEXT also counts: an upright read of the
// same pixels yields more legible content than a sideways one, and that is
// measurable without pretending to score quality.
func transformHelped(orig, alt *Region) bool {
	if clearsAny(orig.Flags, alt.Flags) {
		return true
	}
	return len(strings.TrimSpace(alt.Text)) > len(strings.TrimSpace(orig.Text))
}

// indexOfOverlap finds an existing descent covering substantially the same
// ground, so the same block is not read twice at the cost of two model calls.
func indexOfOverlap(ds []descent, r Rect, iou float64) int {
	for i, d := range ds {
		if d.bbox.overlaps(r) >= iou {
			return i
		}
	}
	return -1
}

// clearsAny reports whether the new flag set dropped any of the old ones — the
// strong half of the progress requirement.
func clearsAny(before, after []string) bool {
	has := func(fs []string, f string) bool {
		for _, x := range fs {
			if x == f {
				return true
			}
		}
		return false
	}
	for _, f := range before {
		if f == FlagExhausted || f == FlagBudget {
			continue
		}
		if !has(after, f) {
			return true
		}
	}
	return false
}

// regionPrompt asks for the two things a node needs. Kept deliberately short:
// the instruction competes with the image for the model's attention, and the
// image is the point.
const regionPrompt = `Look at this image and answer with ONE JSON object, nothing else:

{"description": "...", "kind": "...", "regions": [{"x":0,"y":0,"w":0,"h":0,"rotation":0,"kind":"...","reason":"..."}]}

description: transcribe ALL text you can read, verbatim. Where you cannot read
  text, say what is there instead. Never guess at characters you cannot see.
kind: one of overview, text-block, table, drawing, legend, title-block.
regions: areas worth examining MORE CLOSELY than this view allows — dense
  annotation, small print, a table, a title block. Coordinates are fractions of
  THIS image (0..1). rotation is 0, 90, 180 or 270: what this area must be
  turned by to read upright. Return [] if nothing here needs a closer look, or
  if the whole image is already legible.
Do not propose an area that covers most of this image unless it needs a
different rotation.`

var jsonObjRe = regexp.MustCompile(`(?s)\{.*\}`)

// ParseRegionReading pulls the JSON object out of a model reply.
//
// Tolerant of the wrapping models add — prose before, a fenced block, a trailing
// apology. A reply that carries no object at all is still usable as a
// description: losing the transcription because the JSON was malformed would
// throw away the part that matters.
func ParseRegionReading(s string) RegionReading {
	m := jsonObjRe.FindString(s)
	if m == "" {
		return RegionReading{Description: strings.TrimSpace(s)}
	}
	var out RegionReading
	if err := json.Unmarshal([]byte(m), &out); err != nil {
		return RegionReading{Description: strings.TrimSpace(s)}
	}
	if strings.TrimSpace(out.Description) == "" {
		out.Description = strings.TrimSpace(strings.Replace(s, m, "", 1))
	}
	return out
}

// AskWithOCR builds an Ask that drives a VLM through the existing OCR client,
// so the region walk inherits the page cache, the repetition guard and the
// retry policy already in place.
func (o *OCR) AskWithOCR() func(context.Context, PageImage, int) (RegionReading, error) {
	return func(ctx context.Context, img PageImage, _ int) (RegionReading, error) {
		prev := o.Prompt
		o.Prompt = regionPrompt
		defer func() { o.Prompt = prev }()
		text, _, err := o.PageWithEngine(ctx, img)
		if err != nil {
			// A looped region is not a failed one: the guard fired, which is
			// itself the signal to descend rather than to give up on the page.
			if strings.Contains(err.Error(), "NOT indexed") || strings.Contains(err.Error(), "repeat") {
				return RegionReading{Repeated: true}, nil
			}
			return RegionReading{}, fmt.Errorf("region read: %w", err)
		}
		return ParseRegionReading(text), nil
	}
}
