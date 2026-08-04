package raglit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"regexp"
	"strconv"
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
	// About carries what this region knows about ITSELF and cannot see: what its
	// pixels measured as, and whether it is one cell of a grid. Bundled rather
	// than passed as more parameters, because everything here exists for the same
	// reason — the model is looking at a crop and some of what governs its answer
	// is not in the crop.
	Ask func(ctx context.Context, img PageImage, about RegionAbout) (RegionReading, error)

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
	// MaxEscalations bounds turn 3 for the whole document (0 → 4).
	//
	// Separate from MaxCalls because an escalation is the most expensive call in
	// the walk — it is asked at the PARENT's resolution, which on an E-size sheet
	// is four tokens per square inch, the worst per-token value anywhere in the
	// descent. It should be rare; if it is not, the region proposals are bad and
	// tiling is the cheaper answer.
	MaxEscalations int
	MinRegionIn    float64

	// Hint is what the caller is looking for, threaded into every prompt.
	//
	// The model proposing regions is looking at a view where the thing you want
	// may be physically unresolvable — 6pt bearing labels on a sheet seen at 4
	// tokens per square inch — so it cannot propose what it cannot see. A hint
	// supplies the salience the view cannot: "read every bearing, distance and
	// monument call on the drawing" costs nothing and beats any amount of extra
	// budget, because budget alone only re-asks the same blind view.
	Hint string

	// Tile turns on geometric subdivision of large low-resolution DRAWINGS
	// instead of asking where to look. See tileRegion.
	Tile bool

	calls       int
	escalations int
	seen        map[string]bool // image SHA → already read; this IS the cycle detector
}

// RegionAbout is what a region is told about itself before it is read.
type RegionAbout struct {
	Depth int
	// Damage is what DamageOf measured on these pixels — see damageSuffix. The
	// model is shown a downsample and cannot tell a blurred crop from a small one.
	Damage []string
	// Grid is set when this region is one CELL of a computed grid rather than a
	// block somebody named: "row 2 of 4, column 3 of 4". A cell has neighbours it
	// cannot see and overlaps them, which changes what it should transcribe — see
	// gridSuffix.
	Grid string
	// Escalation is set only on TURN 3: a parent being asked what to do about a
	// child that reported its frame was broken. Present means "answer with an
	// action, not a transcription" — see escalationSuffix.
	Escalation string
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
	// Verdict is a region's claim about its OWN frame, when it has one. Empty
	// means "read" — nothing to report.
	//
	// The line it draws: escalate when the transform is fundamentally broken,
	// refine when the fix needs no knowledge of the larger document. A refinement
	// is expressed as a proposal and handled here; an escalation is the one thing
	// a region cannot do for itself, because the box and the rotation came from
	// its parent.
	Verdict string `json:"verdict"`
	// Action is a PARENT's answer when it is asked what to do about a child that
	// escalated. See escalationPrompt.
	Action string `json:"action"`
	// Because is one line for a human reading the record, on either.
	Because string `json:"because"`
}

// What a region may claim about its own frame, and what a parent may answer.
const (
	VerdictRead      = "read"      // or empty
	VerdictEscalate  = "escalate"  // the frame is wrong and I cannot fix it from here
	VerdictIllegible = "illegible" // right frame, the pixels are not there
	VerdictUnknown   = "unknown"   // the model returned a word this package does not define

	ActionRetransform = "retransform" // box was right, rendering was wrong
	ActionRepick      = "repick"      // box was wrong
	ActionKeep        = "keep"        // the child was mistaken; its reading stands
	ActionAbandon     = "abandon"     // nothing here can be read
)

// RegionProposal is one sub-region the model thinks is worth a closer look.
type RegionProposal struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Rotation int     `json:"rotation"`
	Reason   string  `json:"reason"`
	Kind     string  `json:"kind"`
	// Filter is a repair the model is asking for on the SAME pixels. Named by the
	// model rather than applied by measurement alone: the flags say what is wrong,
	// and the model is the one looking at the region when it decides what to do
	// about it. An unknown name is refused in route rather than ignored here.
	Filter RegionFilter `json:"filter"`
	// Margin is INCHES of extra paper the region wants on every side — a region
	// REFINING its own frame rather than escalating.
	//
	// Escalate when the transform is fundamentally broken; refine when the fix
	// needs no knowledge of the larger document. A word truncated at the region's
	// own edge is the second kind: the region can see the cut, "half an inch more"
	// is a complete statement of the remedy, and asking a parent that cannot see
	// the region's six-point text to adjudicate it buys nothing.
	//
	// Inches, not a fraction, for the reason descentPadIn is a length: half an
	// inch has to mean the same thing at every depth, and a fraction of a small
	// region vanishes exactly where cuts are worst.
	Margin float64 `json:"margin"`
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
	if rr.MaxEscalations == 0 {
		rr.MaxEscalations = 4
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
	err := rr.visit(ctx, page, root, 0, "")
	// Number even a partial tree. A descent that failed half way still produced
	// regions whose text somebody may have to attest, and an unaddressable region
	// is one nobody can be shown.
	root.assignIDs(fmt.Sprintf("p%d", pageNo))
	return root, err
}

// visit reads one region and descends into what it proposes.
// expect is what the PARENT said is in this region, read at the parent's
// resolution. Threaded down rather than looked up because it is the only
// account of the region not produced by the render being judged — see
// transformHelped, which is the one thing that uses it.
func (rr *RegionReader) visit(ctx context.Context, page image.Image, reg *Region, transformsUsed int, expect string) error {
	reg.TokensPerSqIn = resolutionOf(reg.BBox, rr.PageWIn, rr.PageHIn, rr.DPI)
	if reg.TokensPerSqIn < letterTokensPerSqIn*lowResolutionRatio {
		// Known BEFORE the model is called, and on its own enough to distrust
		// whatever comes back.
		reg.addFlag(FlagLowResolution)
	}

	img, err := renderRegion(page, reg.BBox, reg.Rotation, reg.Filter)
	if err != nil {
		return err
	}
	// What is measurably wrong with these PIXELS, taken on the crop as rendered
	// and BEFORE the call — the same discipline as low-resolution. Measured on the
	// filtered image when a filter is in force, which is what makes a repair
	// testable: a contrast transform that clears `faded` cleared it on the bytes
	// the model was handed.
	var damage []string
	if m, _, derr := image.Decode(bytes.NewReader(img)); derr == nil {
		damage = DamageOf(m)
		for _, f := range damage {
			reg.addFlag(f)
		}
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

	reading, err := rr.Ask(ctx, PageImage{Page: reg.Page, Mime: "image/png", Data: img},
		RegionAbout{Depth: reg.Depth, Damage: damage, Grid: reg.Grid})
	if err != nil {
		return err
	}
	reg.Text = reading.Description
	reg.Downscales = reading.Downscales
	if reg.Kind == "" {
		reg.Kind = reading.Kind
	}
	reg.Verdict, reg.Because = normalizeVerdict(reading.Verdict), reading.Because
	if reg.Verdict == VerdictUnknown {
		// A verdict this package does not define. Recorded rather than silently
		// read as "fine": an unimplemented filter is refused visibly for the same
		// reason, and a model inventing vocabulary should not look like a model
		// with nothing to report. Measured — asked for read/escalate/illegible, it
		// also returned "pass" and "accepted".
		reg.addFlag(FlagUnknownVerdict)
		reg.Because = strings.TrimSpace(reading.Verdict + " " + reading.Because)
	}
	if reading.Repeated {
		reg.addFlag(FlagRepetition)
	}
	// The parent described this region before anything was cropped or rotated out
	// of it. A reading that shares nothing with that account is evidence against
	// the TRANSFORM, not against the page — see FlagTransformSuspect.
	//
	// comparable() first, because "nothing to compare" and "nothing matched" are
	// different facts that overlap alone reports as the same 0.0. A parent that
	// said "a sheet" has not disagreed with its child about anything; treating
	// that as disagreement flags every region under a terse account and — once
	// the flag drives escalation — spends the parent's turn on it.
	if comparableExpectation(expect) && strings.TrimSpace(reg.Text) != "" &&
		expectationOverlap(reg.Text, expect) < expectationFloor {
		reg.addFlag(FlagTransformSuspect)
	}

	if reg.Depth >= rr.MaxDepth {
		// Told to stop, not out of budget. See FlagDepthReached.
		reg.addFlag(FlagDepthReached)
		return nil
	}

	descents, transforms := rr.route(reg, reading.Regions)

	// A big low-resolution DRAWING is tiled rather than asked about.
	//
	// This is the case the whole package exists for and the one it was worst at.
	// Region proposals come from a model looking at the region itself, and on a
	// 991 square-inch sheet the root is seen at 4 tokens per square inch against
	// a readable baseline of 39 — a tenth. At that scale block capitals in a
	// title block survive the downscale and 6pt bearing labels do not, so the
	// model proposes the margins, truthfully reports `exhausted`, and the
	// drawing — every bearing, distance, lot line and monument call — is never
	// in any region at any depth. A REGION CANNOT BE PROPOSED BY SOMETHING THAT
	// CANNOT SEE IT, and no budget fixes that: every extra call re-asks the same
	// blind view. Measured: 40 calls gave 23 regions and zero geometry; 200 gave
	// 28 regions and zero geometry.
	//
	// So for an under-resolved region the tiles are computed rather than
	// requested. Deterministic, no salience judgement, and every part of it is
	// covered exactly once instead of by whatever a blind view happened to name.
	//
	// It used to also require kind == "drawing", and that gate did not survive
	// contact with the corpus. Measured 2026-08-03 over every oversize page in it:
	// all 13 were flagged low-resolution — arithmetic, no model call, right every
	// time — and exactly ONE was called a drawing. Eleven came back `overview` and
	// two `text-block`, including four 27x36in recorded surveys that are drawings
	// by any description. Only the one that said `drawing` ever tiled.
	//
	// The gate was asking the blind root what it was looking at. That is the same
	// view this whole branch exists because it cannot be trusted, and `overview`
	// is a compliant answer from the prompt's own list. A sheet that large and
	// that under-resolved needs cutting whatever the model decides to call it, so
	// the half that is measured decides alone.
	if rr.Tile && reg.hasFlag(FlagLowResolution) {
		if tiles := rr.tileRegion(reg); len(tiles) > 0 {
			descents = append(tiles, descents...)
			if len(descents) > rr.MaxChildren {
				// The cap exists to stop a model naming forty overlapping
				// blocks. Tiles are a partition and are not that, so they raise
				// it rather than being truncated by it — cutting them would
				// leave part of the drawing unread while reporting success.
				descents = descents[:len(tiles)]
			}
		}
	}

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
		// The proposal's own geometry, not the parent's box. A refinement that
		// asks for margin and is then rendered at the unchanged bbox would come
		// back identical, be caught by the cycle detector, and read as the model
		// asking for nothing — which is how growth was impossible before.
		box := reg.BBox
		if t.Margin > 0 {
			box = box.paddedIn(t.Margin, rr.PageWIn, rr.PageHIn)
		}
		alt := &Region{Page: reg.Page, BBox: box, Rotation: t.Rotation,
			Filter: t.Filter, Kind: reg.Kind, Depth: reg.Depth}
		if err := rr.visit(ctx, page, alt, transformsUsed+1, expect); err != nil {
			return err
		}
		if alt.hasFlag(FlagCycled) || !transformHelped(reg, alt, expect) {
			// Bought nothing. Two of these in a row end the region as a flagged
			// leaf, which is the honest outcome for six-point text on a scan.
			reg.addFlag(FlagCycled)
			break
		}
		// It helped: keep the better reading and its flags — and the render it
		// came from. Keeping the text without the digest would leave the region
		// claiming its words were read off the image it REPLACED, which is the
		// false attestation this whole record exists to make impossible.
		reg.Text, reg.Flags, reg.Rotation, reg.Filter = alt.Text, alt.Flags, alt.Rotation, alt.Filter
		reg.SHA256, reg.Downscales = alt.SHA256, alt.Downscales
		reg.Children = append(reg.Children, alt.Children...)
	}

	for _, d := range descents {
		if rr.calls >= rr.MaxCalls {
			reg.addFlag(FlagBudget)
			break
		}
		child := &Region{Page: reg.Page, BBox: d.bbox, Rotation: d.rotation,
			Kind: d.kind, Grid: d.grid, Depth: reg.Depth + 1}
		if err := rr.visit(ctx, page, child, 0, reading.Description); err != nil {
			return err
		}
		// The child cannot fix a frame it did not choose. If it says so — or if
		// its reading has nothing to do with what THIS region said is here — the
		// question comes back up to whoever owns the box.
		if rr.wantsEscalation(child) {
			if err := rr.escalate(ctx, page, reg, child, reading.Description); err != nil {
				return err
			}
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
	filter   RegionFilter
	grid     string // set only for computed tiles; see tileRegion
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
			// Pad AFTER mapping into page space, not before. The proposal is in
			// the PARENT's coordinates, so a pad applied there is scaled by the
			// parent's own size on the way out — which is the same
			// shrinks-with-the-region bug in a subtler place.
			padded := parent.BBox.within(r).paddedIn(descentPadIn, rr.PageWIn, rr.PageHIn)
			// Duplicate proposals are common — the model names the same block
			// twice under different reasons — and each one costs a model call.
			if dup := indexOfOverlap(ds, padded, 0.6); dup >= 0 {
				continue
			}
			ds = append(ds, descent{bbox: padded, rotation: p.Rotation, kind: p.Kind})
			continue
		}
		if !ValidRegionFilter(p.Filter) {
			// An invented repair. Refused rather than silently dropped to none, so
			// that a model asking for "denoise" does not look like a model asking
			// for nothing.
			p.Filter = FilterNone
		}
		if p.Margin > maxProposedMarginIn {
			// A region asking for the whole sheet back is escalating in a
			// refinement's clothes. Capped rather than refused: the ask is
			// legitimate, the size is not.
			p.Margin = maxProposedMarginIn
		}
		if p.Rotation != parent.Rotation || p.Filter != parent.Filter || p.Margin > 0 {
			ts = append(ts, p)
		}
		// Same area, same rotation, same filter AND no margin: nothing was asked.
	}
	if len(ds) > rr.MaxChildren {
		ds = ds[:rr.MaxChildren]
	}
	return ds, ts
}

// transformHelped decides whether a re-render earned its place.
//
// Clearing a flag is the strong signal — a narrower field breaking a repetition
// loop, a threshold resolving a disagreement. But a ROTATION cannot clear
// `low-resolution`: the area is unchanged, so the arithmetic is identical, and
// judging rotation on flags alone rejected every rotation including the one that
// recovered the survey's legal description from a sideways text block.
//
// The substitute used to be "more text wins", and MEASURED, that rule is
// backwards. Reading page 2 of the survey four ways (2026-08-03, see
// plan/hierarchical-regions.md), the correct render was the shorter one every
// time:
//
//	whole sheet sideways  9,316 chars   89% of lines duplicated   2 bearings wrong
//	whole sheet upright   2,187 chars    2% duplicated            correct
//	drawing sideways     13,715 chars   94% duplicated            2 facts wrong
//	drawing upright       2,087 chars    3% duplicated            correct
//
// The wrong orientation does not lose text. It makes the model run on, so LENGTH
// rewards exactly the render that failed. What separates them without overlap is
// how much of the output repeats itself, and — where a parent said what is in
// here — whether the reading is about the thing the parent described at all.
func transformHelped(orig, alt *Region, expect string) bool {
	// A render that mostly repeats itself never wins, whatever else it scores.
	// This is the one signal measured with no overlap between the good and bad
	// renders, so it is applied before anything else.
	od, ad := degenerateRatio(orig.Text), degenerateRatio(alt.Text)
	if ad >= degenerateLineRatio && od < degenerateLineRatio {
		return false
	}
	if od >= degenerateLineRatio && ad < degenerateLineRatio {
		return true
	}
	if clearsAny(orig.Flags, alt.Flags) {
		return true
	}
	// What the PARENT said is in here is the only description of this region not
	// produced by the render under judgement, which makes it the one thing a
	// wrong rotation cannot also corrupt. A sideways or upside-down read does not
	// merely garble — it comes back about something else.
	if strings.TrimSpace(expect) != "" {
		if o, a := expectationOverlap(orig.Text, expect), expectationOverlap(alt.Text, expect); o != a {
			return a > o
		}
	}
	// Nothing else to go on: more DISTINCT content, which is the old rule with
	// the repetition that broke it taken out.
	return distinctLen(alt.Text) > distinctLen(orig.Text)
}

// degenerateLineRatio is where a reading stops being a transcription and starts
// being a loop. Measured: correct renders of the survey duplicated 2-3% of their
// lines, the mis-rotated ones 89-94%. Nothing observed between, so the threshold
// is set in the empty middle rather than tuned against either end.
const degenerateLineRatio = 0.5

// expectationFloor is how little a reading may have in common with what its
// parent said is here before the TRANSFORM, not the content, becomes the
// suspect. Deliberately low: a parent describes a region at a resolution where
// it can read the block capitals and not the six-point text, so a correct child
// legitimately shares little with it. What this catches is the child that shares
// NOTHING, which is what a rotation applied the wrong way round produces.
const expectationFloor = 0.05

// normalizedLines splits a reading into comparable lines: trimmed, folded to
// upper case, and dropping the very short ones, which are table rules and stray
// glyphs rather than content.
func normalizedLines(text string) []string {
	var out []string
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.ToUpper(strings.Join(strings.Fields(ln), " "))
		if len(ln) > 3 {
			out = append(out, ln)
		}
	}
	return out
}

// degenerateRatio is the fraction of a reading's lines that repeat one already
// seen. A page with a genuinely repeated call ("FND. R/C MOWRER") scores a few
// percent; a loop scores most of the way to 1.
func degenerateRatio(text string) float64 {
	lines := normalizedLines(text)
	if len(lines) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(lines))
	for _, ln := range lines {
		seen[ln] = struct{}{}
	}
	return 1 - float64(len(seen))/float64(len(lines))
}

// distinctLen is the length of a reading counting each distinct line once.
func distinctLen(text string) int {
	seen := map[string]struct{}{}
	n := 0
	for _, ln := range normalizedLines(text) {
		if _, ok := seen[ln]; ok {
			continue
		}
		seen[ln] = struct{}{}
		n += len(ln)
	}
	return n
}

// comparableExpectation reports whether a parent's account is substantial enough
// for disagreement with it to mean anything.
//
// The overlap measure is built on 5-word shingles, so an account shorter than
// that produces none and scores 0.0 against every child — indistinguishable from
// a child that genuinely came back about somewhere else. A description that
// short is not evidence either way.
func comparableExpectation(expect string) bool {
	return len(strings.Join(strings.Fields(expect), " ")) >= minExpectationChars
}

// minExpectationChars is how much of an account is needed before its absence
// from a child's reading is a fact rather than an artifact.
//
// A LENGTH, because Shingles is character-based — `folded[i:i+w]` — so "a sheet"
// yields three 5-character shingles and a shingle count cannot tell a real
// description from a stub. Sixty characters is about a sentence: the survey's
// own region accounts run to paragraphs, and the parent descriptions that carry
// no information ("a sheet", "a drawing") fall far below it.
const minExpectationChars = 60

// expectationOverlap is the fraction of what the parent said is here that turns
// up in the reading, by the same shingles the corpus-similarity path uses.
//
// Asymmetric on purpose: a child SHOULD say more than its parent did — that is
// the entire point of descending — so what matters is how much of the parent's
// account the child accounts for, not how much of the child the parent predicted.
func expectationOverlap(text, expect string) float64 {
	const w = 5
	want := Shingles(strings.ToUpper(expect), w)
	if len(want) == 0 {
		return 0
	}
	got := map[uint64]struct{}{}
	for _, h := range Shingles(strings.ToUpper(text), w) {
		got[h] = struct{}{}
	}
	hit := 0
	for _, h := range want {
		if _, ok := got[h]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
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

{"description": "...", "kind": "...", "verdict": "", "because": "", "regions": [{"x":0,"y":0,"w":0,"h":0,"rotation":0,"margin":0,"kind":"...","reason":"..."}]}

description: transcribe ALL text you can read, verbatim. Where you cannot read
  text, say what is there instead. Never guess at characters you cannot see.
kind: one of overview, text-block, table, drawing, legend, title-block.
regions: areas worth examining MORE CLOSELY than this view allows — dense
  annotation, small print, a table, a title block. Coordinates are fractions of
  THIS image (0..1). rotation is 0, 90, 180 or 270: what this area must be
  turned by to read upright. Return [] if nothing here needs a closer look, or
  if the whole image is already legible.
Do not propose an area that covers most of this image unless it needs a
different rotation.

If this image cannot be read AS FRAMED and more paper would not fix it — it is
upside down or mirrored, or it plainly shows something other than what you were
told is here — answer with "verdict":"escalate" and say why in "because". Do not
guess at it and do not propose regions: the area and the rotation were chosen
outside this view, and correcting them is not yours to do. If it is framed fine
but simply too coarse to resolve at any treatment, answer "verdict":"illegible".

IF TEXT IS CUT OFF AT THE EDGE OF THIS IMAGE — a word ending mid-letters against
the border, a line running off the side — say so by proposing THIS WHOLE IMAGE
(x:0,y:0,w:1,h:1) with "margin" set to the inches of extra paper you need, up to
2. That is not a request to look somewhere else; it is this same view with more
of the page around it. Use it only when you can see the cut. If instead the image
looks wrong in a way MORE PAPER WOULD NOT FIX — upside down, or showing something
other than what you were told is here — propose nothing and say so in
description, because that is not yours to correct.`

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
func (o *OCR) AskWithOCR() func(context.Context, PageImage, RegionAbout) (RegionReading, error) {
	return o.AskWithHint("")
}

// AskWithHint is AskWithOCR with a caller's statement of what is being looked
// for appended to whichever prompt applies.
//
// Appended rather than woven in: the instruction competes with the image for the
// model's attention, and a hint that rewrites the whole prompt would also change
// the parts that make the answer parseable.
func (o *OCR) AskWithHint(hint string) func(context.Context, PageImage, RegionAbout) (RegionReading, error) {
	suffix := ""
	if h := strings.TrimSpace(hint); h != "" {
		suffix = "\n\nWHAT THE CALLER IS LOOKING FOR: " + h +
			"\nIf this image contains any of it, transcribe that FIRST and in full, and " +
			"propose regions covering wherever more of it appears."
	}
	return func(ctx context.Context, img PageImage, about RegionAbout) (RegionReading, error) {
		prev := o.Prompt
		// The root is asked to account for the whole sheet; a crop is asked to
		// transcribe. Same walk, different question, decided by where we are in it.
		switch {
		case about.Escalation != "":
			// Turn 3 asks for a decision, not a reading — a different question
			// entirely, so it replaces the prompt rather than appending to it.
			o.Prompt = escalationSuffix(about.Escalation)
		case about.Depth == 0:
			o.Prompt = rootPrompt + suffix + damageSuffix(about.Damage) + gridSuffix(about.Grid)
		default:
			o.Prompt = regionPrompt + suffix + damageSuffix(about.Damage) + gridSuffix(about.Grid)
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

// tileRegion cuts a region into pieces that each clear the readable baseline.
//
// The grid is sized from RESOLUTION, not from a fixed count: enough tiles that
// each one is seen at letter-page detail, since that is the threshold below
// which a reading cannot be trusted whatever it returns. A 991 square-inch sheet
// at 4 tokens/in² needs roughly ten times the detail, so about a 4x4 grid — and
// with the pad, neighbouring tiles overlap, so a bearing label lying on a seam
// is read whole by one of them.
//
// Capped, because the point is to make the sheet legible rather than to spend
// the budget: 6x6 is 36 calls, which is already more than most sheets deserve.
func (rr *RegionReader) tileRegion(reg *Region) []descent {
	if reg.TokensPerSqIn <= 0 {
		return nil
	}
	// How much more detail each tile needs than the region got.
	want := letterTokensPerSqIn / reg.TokensPerSqIn
	if want <= 1 {
		return nil
	}
	n := int(math.Ceil(math.Sqrt(want)))
	n = min(max(n, 2), 6)

	wIn, hIn := reg.BBox.W*rr.PageWIn, reg.BBox.H*rr.PageHIn
	if wIn/float64(n) < rr.MinRegionIn || hIn/float64(n) < rr.MinRegionIn {
		return nil // already small enough that another cut buys no resolution
	}

	var out []descent
	tw, th := reg.BBox.W/float64(n), reg.BBox.H/float64(n)
	for row := range n {
		for col := range n {
			t := Rect{
				X: reg.BBox.X + float64(col)*tw,
				Y: reg.BBox.Y + float64(row)*th,
				W: tw, H: th,
			}.paddedIn(descentPadIn, rr.PageWIn, rr.PageHIn)
			out = append(out, descent{bbox: t, rotation: reg.Rotation, kind: "drawing",
				grid: fmt.Sprintf("row %d of %d, column %d of %d", row+1, n, col+1, n)})
		}
	}
	return out
}

// ReadInto re-reads ONE recorded region and grafts the result back into the tree.
//
// The alternative on offer was raising --depth, which re-runs the whole page and
// re-derives the same split — so a tree that spent its budget on the margins
// could never be sent back into the drawing. Everything needed was already
// recorded: the bbox, the rotation and the dpi identify the crop exactly, which
// is the property `raglit region` uses to put the same pixels back on screen.
//
// The subtree is REPLACED rather than merged. A re-read is a new reading, and
// keeping the old children beside the new ones would leave two accounts of the
// same ground with nothing to say which was current.
func (rr *RegionReader) ReadInto(ctx context.Context, page image.Image, pageNo int, docPath, regionID string) (*Region, error) {
	doc, ok, err := ReadRegionDoc(docPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("raglit: no region read recorded for %s", docPath)
	}
	rp, ok := doc.PageRead(pageNo)
	if !ok || rp.Root == nil {
		return nil, fmt.Errorf("raglit: no recorded read of page %d", pageNo)
	}
	target := findRegion(rp.Root, regionID)
	if target == nil {
		return nil, fmt.Errorf("raglit: page %d has no region %q (try `raglit region --list %s`)",
			pageNo, regionID, docPath)
	}
	if rr.MaxCalls == 0 {
		rr.MaxCalls = 40
	}
	if rr.MaxChildren == 0 {
		rr.MaxChildren = 8
	}
	if rr.MaxTransforms == 0 {
		rr.MaxTransforms = 2
	}
	if rr.MaxEscalations == 0 {
		rr.MaxEscalations = 4
	}
	if rr.MinRegionIn == 0 {
		rr.MinRegionIn = 1.0
	}
	rr.seen = map[string]bool{}
	rr.calls = 0

	fresh := &Region{Page: pageNo, BBox: target.BBox, Rotation: target.Rotation,
		Kind: target.Kind, Depth: target.Depth}
	if err := rr.visit(ctx, page, fresh, 0, ""); err != nil {
		return nil, err
	}
	*target = *fresh
	rp.Root.assignIDs(fmt.Sprintf("p%d", pageNo))
	return rp.Root, nil
}

// findRegion locates a region by its recorded id.
func findRegion(root *Region, id string) *Region {
	for _, r := range root.Flatten() {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// damageSuffix tells the model what was measured about the pixels it is being
// shown, and what it may ask for about it.
//
// This is the half the measurement cannot do. Measured 2026-08-03: the variance
// of the laplacian identifies a blurred crop that the model itself diagnoses as
// "skew" with 0.9 confidence — and the deskew it then prescribes makes a blurred
// region WORSE. So the number is what notices, and the model is what decides,
// because it is the one that can see whether the region is a faded fax or a
// drawing that is mostly white space.
func damageSuffix(damage []string) string {
	if len(damage) == 0 {
		return ""
	}
	var what []string
	for _, d := range damage {
		switch d {
		case FlagBlurred:
			what = append(what, "its strokes measure as SMEARED (low edge energy)")
		case FlagFaded:
			what = append(what, "its tones measure as CRUSHED into a narrow band")
		}
	}
	if len(what) == 0 {
		return ""
	}
	return "\n\nMEASURED ABOUT THIS IMAGE: " + strings.Join(what, ", and ") + "." +
		"\nThese are measurements of the pixels, not a judgement about the document." +
		"\nIf a repair would help, propose THIS SAME AREA (x:0,y:0,w:1,h:1) with a" +
		" \"filter\" of \"contrast\" or \"sharpen\". Propose nothing if the region reads" +
		" fine as it is — a repair that recovers nothing is discarded anyway."
}

// gridSuffix tells a computed tile that it is one, and what that obliges it to
// leave alone.
//
// A cell cannot see its neighbours and OVERLAPS them by descentPadIn, so an item
// near a seam is visible — often partly — from both sides. Without a rule, both
// cells guess, and the two failure modes are not equal: a duplicated monument
// call is recoverable by anyone reading the transcript, a dropped one is
// invisible and this whole package exists because a dropped clause read as a
// complete document.
//
// So the rule is asymmetric on purpose. At the 45% threshold NOTHING can be
// dropped by geometry: if this cell holds under 45% of an item, the neighbour
// necessarily holds over 55% and takes it. Duplicates are confined to the narrow
// band where both sides hold 45-55%, and are accepted as the cost.
func gridSuffix(grid string) string {
	if strings.TrimSpace(grid) == "" {
		return ""
	}
	return "\n\nTHIS IMAGE IS ONE CELL OF A GRID over a larger sheet — " + grid + "." +
		"\nThe neighbouring cells overlap this one, so text at your edges also appears in them." +
		"\nTranscribe an item only if you can see AT LEAST 45% of it. If less than that is" +
		" visible, leave it out entirely rather than guessing at it: the neighbouring cell" +
		" holds the rest and will transcribe it whole." +
		"\nThis will occasionally put the same item in two cells. That is intended and" +
		" harmless. Inventing the hidden half of one is not."
}

// Turn 3 — the parent decides what to do about a child that cannot fix itself.
//
// Escalate when the transform is fundamentally broken; refine when the fix needs
// no knowledge of the larger document. A refinement never reaches here: it is a
// proposal the child makes about its own frame and route handles it as a
// transform. What reaches here is the other kind — a box in the wrong place, a
// frame the child cannot tell is upside down — where the remedy needs something
// the child structurally does not have, namely where its box came from.
//
// It is the PARENT's turn, at the parent's resolution, and the parent adds no
// information about the child's characters: on an E-size sheet it is looking at
// four tokens per square inch. So the actions it may take are all about
// GEOMETRY, and `keep` means the child's reading stands — never "here is a
// better one", because anything it transcribed would be the low-resolution kind
// this whole package exists to distrust.

// wantsEscalation reports whether a child is asking for its parent, either by
// saying so or by coming back about somewhere else entirely.
func (rr *RegionReader) wantsEscalation(child *Region) bool {
	if rr.escalations >= rr.MaxEscalations || rr.calls >= rr.MaxCalls {
		return false
	}
	return child.Verdict == VerdictEscalate || child.hasFlag(FlagTransformSuspect)
}

// escalate asks the parent what to do, and applies the answer.
func (rr *RegionReader) escalate(ctx context.Context, page image.Image,
	parent, child *Region, expect string) error {
	rr.escalations++

	img, err := renderRegion(page, parent.BBox, parent.Rotation, parent.Filter)
	if err != nil {
		return err
	}
	rr.calls++
	reading, err := rr.Ask(ctx, PageImage{Page: parent.Page, Mime: "image/png", Data: img},
		RegionAbout{Depth: parent.Depth, Grid: parent.Grid,
			Escalation: escalationQuestion(parent, child)})
	if err != nil {
		return err
	}
	// Append, never overwrite. The child's reason for escalating and the parent's
	// reason for its answer are two different facts, and the child said its one
	// first — clobbering it leaves a record of a decision with no account of what
	// prompted it.
	if b := strings.TrimSpace(reading.Because); b != "" {
		if child.Because != "" {
			child.Because += " / parent: " + b
		} else {
			child.Because = "parent: " + b
		}
	}

	switch reading.Action {
	case ActionRetransform, ActionRepick:
		props := rr.route1(parent, child, reading.Regions)
		if len(props) == 0 {
			// It chose an action and gave nothing to act on.
			child.addFlag(FlagEscalated)
			return nil
		}
		// Replace the subtree rather than merging: a re-pick is a new reading of
		// different ground, and keeping both would leave two accounts with
		// nothing to say which is current.
		fresh := &Region{Page: parent.Page, BBox: props[0].bbox, Rotation: props[0].rotation,
			Filter: props[0].filter, Kind: child.Kind, Grid: child.Grid, Depth: child.Depth}
		if err := rr.visit(ctx, page, fresh, 0, expect); err != nil {
			return err
		}
		// It only earns the replacement if it did better by the same rule a
		// transform is held to.
		if !fresh.hasFlag(FlagCycled) && transformHelped(child, fresh, expect) {
			*child = *fresh
			child.addFlag(FlagEscalated)
			return nil
		}
		child.addFlag(FlagEscalated)
		child.addFlag(FlagCycled)
	case ActionAbandon:
		child.addFlag(FlagAbandoned)
	default: // ActionKeep, or anything unrecognised
		child.addFlag(FlagEscalated)
	}
	return nil
}

// route1 turns the parent's answer into at most one re-render of the child's
// ground, in PAGE coordinates. The parent answers in ITS own 0..1 frame, which
// is why this cannot reuse the child's routing.
func (rr *RegionReader) route1(parent, child *Region, props []RegionProposal) []descent {
	var out []descent
	for _, p := range props {
		r := p.rect().clampToUnit()
		if !r.valid() {
			continue
		}
		box := parent.BBox.within(r)
		if p.Margin > 0 {
			if p.Margin > maxProposedMarginIn {
				p.Margin = maxProposedMarginIn
			}
			box = box.paddedIn(p.Margin, rr.PageWIn, rr.PageHIn)
		}
		if wIn, hIn := box.W*rr.PageWIn, box.H*rr.PageHIn; wIn < rr.MinRegionIn && hIn < rr.MinRegionIn {
			continue
		}
		if !ValidRegionFilter(p.Filter) {
			p.Filter = FilterNone
		}
		out = append(out, descent{bbox: box, rotation: p.Rotation, kind: p.Kind, filter: p.Filter})
		break // one re-render per escalation; the budget is the point
	}
	return out
}

// escalationQuestion states the case: what was tried, and what came back.
func escalationQuestion(parent, child *Region) string {
	tried := fmt.Sprintf("rotation %d", child.Rotation)
	if child.Filter != FilterNone {
		tried += ", filter " + string(child.Filter)
	}
	// The child's box in the PARENT's frame, which is the frame it will answer in.
	var rel Rect
	if parent.BBox.W > 0 && parent.BBox.H > 0 {
		rel = Rect{
			X: (child.BBox.X - parent.BBox.X) / parent.BBox.W,
			Y: (child.BBox.Y - parent.BBox.Y) / parent.BBox.H,
			W: child.BBox.W / parent.BBox.W,
			H: child.BBox.H / parent.BBox.H,
		}
	}
	q := fmt.Sprintf("A closer look at x:%.2f y:%.2f w:%.2f h:%.2f of this image, %s, "+
		"came back reporting that it could not be read as framed.", rel.X, rel.Y, rel.W, rel.H, tried)
	if b := strings.TrimSpace(child.Because); b != "" {
		q += " It said: " + strconv.Quote(b) + "."
	}
	if t := strings.TrimSpace(child.Text); t != "" {
		if len(t) > 300 {
			t = t[:300] + "…"
		}
		q += " What it did read was: " + strconv.Quote(t) + "."
	}
	return q
}

// escalationSuffix asks for a decision about GEOMETRY, and forbids a
// transcription.
//
// The parent is looking at its own region, which on an oversize sheet is the
// view that cannot resolve six-point text — so anything it transcribed here
// would be exactly the low-resolution invention this package exists to prevent.
// `keep` means the child's reading stands, never "here is a better one".
func escalationSuffix(q string) string {
	if strings.TrimSpace(q) == "" {
		return ""
	}
	return "\n\nSOMETHING WENT WRONG WITH A CLOSER LOOK AT PART OF THIS IMAGE.\n" + q +
		"\n\nAnswer with ONE JSON object, nothing else:\n" +
		`{"action": "...", "regions": [{"x":0,"y":0,"w":0,"h":0,"rotation":0,"margin":0,"filter":"","kind":"..."}], "because": "..."}` +
		"\naction is one of:" +
		"\n  retransform — the area was right but rendered wrong; give the SAME area with a different rotation, filter or margin." +
		"\n  repick      — the area was wrong; give the area that should have been looked at instead." +
		"\n  keep        — the closer look was mistaken and what it read stands." +
		"\n  abandon     — there is nothing readable there at any treatment." +
		"\nregions: exactly one area, in fractions of THIS image (0..1), for retransform or repick. Empty otherwise." +
		"\nDO NOT TRANSCRIBE ANYTHING. You are looking at this area at a scale that cannot resolve its small text —" +
		" that is why a closer look was taken. Decide where to look and how; the closer look does the reading."
}

// normalizeVerdict maps what came back onto what this package acts on.
//
// Empty and "read" both mean nothing to report. Anything else it does not define
// becomes VerdictUnknown, which is recorded rather than treated as agreement —
// the caller cannot tell a model that said "fine" from one that said a word
// nobody implemented unless the difference is kept.
func normalizeVerdict(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", VerdictRead:
		return ""
	case VerdictEscalate:
		return VerdictEscalate
	case VerdictIllegible:
		return VerdictIllegible
	default:
		return VerdictUnknown
	}
}
