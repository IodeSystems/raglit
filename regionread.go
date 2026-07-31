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

	// MaxDepth bounds descent. 0 means READ THE SHEET AND STOP, which is the
	// default: the root reading is written to account for the whole sheet, so
	// descending is for when a specific area needs the characters, not something
	// every page should pay for. A survey descends into a title block because
	// somebody asked about the certificate number, not because it was scanned.
	MaxDepth      int
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
	// Downscales is how many times the reader shrank the image before the model
	// could read it. Reported back so the region can record what was actually
	// looked at rather than what was handed over.
	Downscales int `json:"-"`
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
	err := rr.visit(ctx, page, root, 0)
	// Number even a partial tree. A descent that failed half way still produced
	// regions whose text somebody may have to attest, and an unaddressable region
	// is one nobody can be shown.
	root.assignIDs(fmt.Sprintf("p%d", pageNo))
	return root, err
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
	// What this region was rendered from, kept rather than discarded: the crop
	// geometry alone does not identify an image, and a human attesting a quote
	// has to be shown the image, not something with the same coordinates.
	reg.DPI, reg.SHA256 = rr.DPI, sha
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
	reg.Downscales = reading.Downscales
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
		// It helped: keep the better reading and its flags — and the render it
		// came from. Keeping the text without the digest would leave the region
		// claiming its words were read off the image it REPLACED, which is the
		// false attestation this whole record exists to make impossible.
		reg.Text, reg.Flags, reg.Rotation = alt.Text, alt.Flags, alt.Rotation
		reg.SHA256, reg.Downscales = alt.SHA256, alt.Downscales
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

// rootPrompt is what the WHOLE SHEET is asked, and it asks a different question
// from the one asked of a crop.
//
// A crop is asked to transcribe: it exists because something was too small to
// read, and the answer wanted is the characters. The root is not that. On a plan
// sheet the entire page IS the drawing, and a root reading that only transcribes
// returns the marginal notes and says nothing about the thing the sheet is FOR —
// which parcels, which boundary, what the drawing asserts. That is how a record
// of survey came back described as "a vicinity map showing a grid of sections":
// true of one inset in a corner, silent about the survey.
//
// So the root is asked to ACCOUNT for the sheet: everything on it, named, in one
// place. Comprehensive at this level is what makes descending optional — a
// reader who never descends still knows what the sheet holds, and a reader who
// does knows where to go.
//
// It still transcribes, because a description that drops the legal text would
// trade one kind of incompleteness for another.
const rootPrompt = `Look at this whole sheet and answer with ONE JSON object, nothing else:

{"description": "...", "kind": "...", "regions": [{"x":0,"y":0,"w":0,"h":0,"rotation":0,"kind":"...","reason":"..."}]}

description: an ACCOUNT OF THE ENTIRE SHEET. Name every distinct thing on it —
  each drawing, map, table, legend, title block, certificate, signature block,
  stamp and note — and say where it sits and what it shows. For a drawing say
  what is depicted: what is bounded, what is labelled, what the lines and
  annotations assert. Then transcribe the text you can read, verbatim. Where
  text is too small to read, say so and say what kind of text it is. Never
  guess at characters you cannot see. Completeness matters more than brevity:
  a reader who sees only this description should know everything the sheet
  contains.
kind: one of overview, text-block, table, drawing, legend, title-block.
regions: areas a closer look WOULD help with — dense annotation, small print, a
  table, a title block. Coordinates are fractions of THIS image (0..1).
  rotation is 0, 90, 180 or 270: what this area must be turned by to read
  upright. Naming an area here does not read it; it records where detail is.`

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
	return func(ctx context.Context, img PageImage, depth int) (RegionReading, error) {
		prev := o.Prompt
		// The root is asked to account for the whole sheet; a crop is asked to
		// transcribe. Same walk, different question, decided by where we are in it.
		if depth == 0 {
			o.Prompt = rootPrompt
		} else {
			o.Prompt = regionPrompt
		}
		defer func() { o.Prompt = prev }()
		text, _, shrinks, err := o.PageAsSeen(ctx, img)
		if err != nil {
			// A looped region is not a failed one: the guard fired, which is
			// itself the signal to descend rather than to give up on the page.
			if strings.Contains(err.Error(), "NOT indexed") || strings.Contains(err.Error(), "repeat") {
				return RegionReading{Repeated: true, Downscales: shrinks}, nil
			}
			return RegionReading{}, fmt.Errorf("region read: %w", err)
		}
		out := ParseRegionReading(text)
		out.Downscales = shrinks
		return out, nil
	}
}
