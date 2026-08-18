package raglit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
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

	// Segment cuts along measured ink clusters instead of the geometric grid.
	// OFF by default: measured worse on this corpus — see withTiles.
	Segment bool

	// Layout tunes cluster finding when Segment is set. Zero value derives its
	// distances from DPI.
	Layout LayoutOpts

	// Tile turns on geometric subdivision of large low-resolution DRAWINGS
	// instead of asking where to look. See tileRegion.
	Tile bool

	// Doc is the document these regions belong to, carried only so the trace can
	// say which one. A walk reads one document; the record may hold many.
	Doc string

	// Trace, when non-nil, receives one line per TURN and per decision taken
	// about it: what was read, at what resolution, what it proposed, what was
	// routed to a descent and what to a transform, what escalated and what the
	// parent said. A tree printed at the end says where the walk ARRIVED; this
	// says what it did on the way, which is the part that explains a bad tree.
	Trace io.Writer

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

	// Doc, Page, RegionID and TokensPerSqIn identify WHICH crop of WHICH page of
	// WHICH document this call is reading.
	//
	// They exist for the trace, and they are what makes a descent observable at
	// all. Without them every call in a walk looks identical in the record —
	// same model, same document, forty images — so a tree that spent its budget
	// in the margins is indistinguishable from one that read the title block,
	// and neither can be joined back to the region it came from. The region id
	// is the join key to the recorded tree; tokens/in² is the number that says
	// whether the crop could have been read at all.
	Doc           string
	Page          int
	RegionID      string
	TokensPerSqIn float64

	// FitsWhole means this region needs no subdivision: every pixel reaches the
	// model in ONE canvas at this resolution. The root is then asked to
	// TRANSCRIBE rather than to account for the sheet and propose where to look.
	//
	// Salience is only worth paying for when something will act on it. Measured
	// on the bench's 94 sq in record of survey: the root prompt returned 4 of 7
	// checks where a plain read of the same pixels returned 7 of 7 — it described
	// the sheet and proposed regions nobody descended into, because there was
	// nothing to descend into. The three it lost were recording numbers.
	FitsWhole bool
}

// RegionReading is what the model returns for one region.
type RegionReading struct {
	// Transcription is the characters on the page, verbatim, markdown for
	// STRUCTURE only. It feeds the full-text index, where someone types an exact
	// recording number and expects this document back.
	//
	// Split from Description on 2026-08-06 because one field could not be both.
	// Its doc comment used to admit as much — "a transcription for a text block,
	// a summary for a drawing" — and every tile of a survey came back
	// kind:drawing, so every tile returned a summary. Six tiles at 43-46
	// tokens/in², above the readable baseline, and not one transcribed a
	// character.
	Transcription string `json:"transcription_markdown"`
	// Description is what this IS, for the index that answers a searcher who
	// does not know the words on the page.
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
// readRegion renders a region, measures it, and asks about it — and does NOTHING
// else. No routing, no tiling, no descent.
//
// Split out because a TRANSFORM candidate needs exactly this and nothing more.
// It used to go through the whole of visit, which meant an alt of a tileable
// region was itself low-resolution, tiled, and spent sixteen calls proving a
// speculative rotation was worse. Measured over the corpus: three sheets ran
// their real descent out of budget after four tiles of sixteen, and those three
// are the only pages in the sweep where tiling did WORSE than not tiling
// (-32%, -28%, -63%) while every sheet that finished its grid gained.
//
// ok is false when the region was not read at all — a cycle or a spent budget —
// and the caller must not treat what it holds as a reading.
func (rr *RegionReader) readRegion(ctx context.Context, page image.Image, reg *Region,
	expect string) (RegionReading, bool, error) {
	reg.TokensPerSqIn = resolutionOf(reg.BBox, rr.PageWIn, rr.PageHIn, rr.DPI)
	if reg.TokensPerSqIn < letterTokensPerSqIn*lowResolutionRatio {
		reg.addFlag(FlagLowResolution)
	}
	img, err := renderRegion(page, reg.BBox, reg.Rotation, reg.Filter)
	if err != nil {
		return RegionReading{}, false, err
	}
	// What is measurably wrong with these PIXELS, taken on the crop as rendered
	// and BEFORE the call — the same discipline as low-resolution. Measured on the
	// filtered image when a filter is in force, which is what makes a repair
	// testable: a contrast transform that clears `faded` cleared it on the bytes
	// the model was handed.
	var damage []string
	if m, _, derr := image.Decode(bytes.NewReader(img)); derr == nil {
		// Same decode as the damage flags: where this crop's ink CLUSTERS, which
		// is what a descent cuts along instead of a blind grid.
		if rr.Segment {
			lo := rr.Layout
			if lo.DPI == 0 {
				lo.DPI = rr.DPI
			}
			reg.Clusters = LayoutClusters(m, lo)
		}
		damage = DamageOf(m)
		for _, f := range damage {
			reg.addFlag(f)
		}
	}
	sha := imageSHA(img)
	reg.DPI, reg.SHA256 = rr.DPI, sha
	if rr.seen[sha] {
		reg.addFlag(FlagCycled)
		return RegionReading{}, false, nil
	}
	if rr.calls >= rr.MaxCalls {
		reg.addFlag(FlagBudget)
		return RegionReading{}, false, nil
	}
	rr.seen[sha] = true
	rr.calls++

	reading, err := rr.Ask(ctx, PageImage{Page: reg.Page, Mime: "image/png", Data: img},
		RegionAbout{Depth: reg.Depth, Damage: damage, Grid: reg.Grid,
			Doc: rr.Doc, Page: reg.Page, RegionID: regionLabel(reg.ID),
			TokensPerSqIn: reg.TokensPerSqIn,
			FitsWhole:     rr.fitsWhole(reg)})
	if err != nil {
		return RegionReading{}, false, err
	}
	reg.Text = reading.text()
	reg.Downscales = reading.Downscales
	if reg.Kind == "" {
		reg.Kind = reading.Kind
	}
	reg.Verdict, reg.Because = normalizeVerdict(reading.Verdict), reading.Because
	if reg.Verdict == VerdictUnknown {
		reg.addFlag(FlagUnknownVerdict)
		reg.Because = strings.TrimSpace(reading.Verdict + " " + reading.Because)
	}
	if reading.Repeated {
		reg.addFlag(FlagRepetition)
	}
	if comparableExpectation(expect) && strings.TrimSpace(reg.Text) != "" &&
		expectationOverlap(reg.Text, expect) < expectationFloor {
		reg.addFlag(FlagTransformSuspect)
	}
	return reading, true, nil
}

// visit reads one region and descends into what it proposes.
// expect is what the PARENT said is in this region, read at the parent's
// resolution. Threaded down rather than looked up because it is the only
// account of the region not produced by the render being judged — see
// transformHelped, which is the one thing that uses it.
func (rr *RegionReader) visit(ctx context.Context, page image.Image, reg *Region,
	transformsUsed int, expect string) error {
	rr.tracef(reg.Depth, "read %s bbox=%.2f,%.2f %.2fx%.2f rot=%d filter=%q %.1f tok/in²%s",
		regionLabel(reg.ID), reg.BBox.X, reg.BBox.Y, reg.BBox.W, reg.BBox.H,
		reg.Rotation, string(reg.Filter), reg.TokensPerSqIn,
		map[bool]string{true: " [tile " + reg.Grid + "]"}[reg.Grid != ""])
	reading, ok, err := rr.readRegion(ctx, page, reg, expect)
	if err != nil {
		// A region that could not be read is not a region that cannot be CUT.
		//
		// Measured on olmOCR-bench: a 66 x 55 inch sheet is refused by the
		// transport, and because the descent read the whole region before tiling
		// it, the one mechanism built for sheets too big to read in one look died
		// on the request that being too big causes. The read is where the account
		// comes from, so losing it costs coverage — but losing the tiles as well
		// costs everything.
		if !rr.canTile(reg) {
			return err
		}
		reg.addFlag(FlagUnread)
		reading = RegionReading{}
		ok = true
	}
	if !ok {
		return nil
	}
	if reg.Depth >= rr.MaxDepth {
		// Told to stop, not out of budget. See FlagDepthReached.
		reg.addFlag(FlagDepthReached)
		return nil
	}

	descents, transforms := rr.route(reg, reading.Regions)
	rr.tracef(reg.Depth, "  -> %d chars, flags=%v verdict=%q; %d proposal(s) -> %d descent(s), %d transform(s)",
		len(reg.Text), reg.Flags, reg.Verdict, len(reading.Regions), len(descents), len(transforms))

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
	if before := len(descents); true {
		descents = rr.withTiles(reg, descents)
		if n := len(descents) - before; n > 0 {
			rr.tracef(reg.Depth, "  -> tiled: %d computed tiles prepended (blind view, no salience asked)", n)
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
			Filter: t.Filter, Kind: reg.Kind, Grid: reg.Grid, Depth: reg.Depth}
		// READ ONLY. A candidate is judged on its own reading; descending into one
		// spends the region's budget proving something that may be discarded.
		altReading, ok, rerr := rr.readRegion(ctx, page, alt, expect)
		if rerr != nil {
			return rerr
		}
		transformsUsed++
		if !ok || !transformHelped(reg, alt, expect) {
			// Bought nothing. Two of these in a row end the region as a flagged
			// leaf, which is the honest outcome for six-point text on a scan.
			reg.addFlag(FlagCycled)
			break
		}
		// It helped: keep the better reading and its flags — and the render it
		// came from, GEOMETRY INCLUDED. Keeping the text and the digest without
		// the bbox left the region claiming words read off an image its own
		// coordinates do not produce: RerenderRegion rebuilt the old crop and
		// VerifyRegionRender refused it. A refinement that grows the frame is
		// exactly the case, and it silently broke the attestation this whole
		// record exists for.
		reg.Text, reg.Flags, reg.Rotation, reg.Filter = alt.Text, alt.Flags, alt.Rotation, alt.Filter
		reg.BBox, reg.SHA256, reg.Downscales = alt.BBox, alt.SHA256, alt.Downscales
		reg.Verdict, reg.Because = alt.Verdict, alt.Because
		// The region now says something different, so what it proposes to do next
		// is different too. Re-route from the adopted reading rather than acting
		// on proposals made about an image that was replaced.
		descents, _ = rr.route(reg, altReading.Regions)
		if before := len(descents); true {
			descents = rr.withTiles(reg, descents)
			if n := len(descents) - before; n > 0 {
				rr.tracef(reg.Depth, "  -> tiled: %d computed tiles prepended (blind view, no salience asked)", n)
			}
		}
	}

	for _, d := range descents {
		if rr.calls >= rr.MaxCalls {
			reg.addFlag(FlagBudget)
			break
		}
		child := &Region{Page: reg.Page, BBox: d.bbox, Rotation: d.rotation,
			Kind: d.kind, Grid: d.grid, Computed: d.computed, Depth: reg.Depth + 1}
		if err := rr.visit(ctx, page, child, 0, reading.expectation()); err != nil {
			return err
		}
		// The child cannot fix a frame it did not choose. If it says so — or if
		// its reading has nothing to do with what THIS region said is here — the
		// question comes back up to whoever owns the box.
		if rr.wantsEscalation(reg, child) {
			rr.tracef(reg.Depth, "  -> escalating %s (verdict=%q suspect=%v), budget %d/%d",
				regionLabel(child.ID), child.Verdict, child.hasFlag(FlagTransformSuspect),
				rr.escalations, rr.MaxEscalations)
			if err := rr.escalate(ctx, page, reg, child, reading.expectation()); err != nil {
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
	grid     string // set only for computed TILES; see tileRegion
	// computed marks a box this package DERIVED rather than a model proposed —
	// a grid tile or a layout cluster. Such a box does not escalate: its frame
	// came from arithmetic over the pixels. grid alone cannot carry this now
	// clusters exist: a cluster is computed but is NOT one cell of a grid and
	// must not be told the 45% overlap rule.
	computed bool
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

// regionPrompt asks for the two things a node needs. Kept deliberately short:
// the instruction competes with the image for the model's attention, and the
// image is the point.

// expectation is everything the parent said about this ground, for the overlap
// test that judges whether a re-render helped.
//
// BOTH halves, because before transcription and description were split this was
// one field carrying both, and the measure is word overlap — narrowing it to
// either half alone would quietly weaken the one signal a wrong rotation cannot
// corrupt.
// text is the transcription, falling back to description.
//
// On the TYPE rather than in ParseRegionReading because a RegionReading is also
// constructed directly — every test's Ask does, and so may any caller wiring its
// own reader — and a fallback that lives in one constructor silently returns
// empty for all the others.
func (r RegionReading) text() string {
	if t := strings.TrimSpace(r.Transcription); t != "" {
		return t
	}
	return strings.TrimSpace(r.Description)
}

func (r RegionReading) expectation() string {
	return strings.TrimSpace(r.Description + " " + r.Transcription)
}

var jsonObjRe = regexp.MustCompile(`(?s)\{.*\}`)

// firstJSONObject returns the first BALANCED {...} in s, or "".
//
// jsonObjRe is greedy from the first brace to the last, which is right for an
// object wrapped in prose and WRONG for the array models actually return:
// `[{...},{...}]` matches as `{...},{...}`, which is not valid JSON, so the
// unmarshal failed and the whole raw reply — braces, escapes and all — became
// the region's text. Observed on every region of a survey read; it is why a
// tree of readings looked like a tree of JSON.
//
// Brace counting rather than a cleverer regex because strings inside the object
// legitimately contain braces, and a transcription of a plat note may contain
// anything at all.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// ParseRegionReading pulls the JSON object out of a model reply.
//
// Tolerant of the wrapping models add — prose before, a fenced block, a trailing
// apology. A reply that carries no object at all is still usable as a
// description: losing the transcription because the JSON was malformed would
// throw away the part that matters.
func ParseRegionReading(s string) RegionReading {
	m := firstJSONObject(s)
	if m == "" {
		t := strings.TrimSpace(s)
		return RegionReading{Transcription: t, Description: t}
	}
	var out RegionReading
	if err := json.Unmarshal([]byte(m), &out); err != nil {
		t := strings.TrimSpace(s)
		return RegionReading{Transcription: t, Description: t}
	}
	// A model that ignores the new field, or a reading recorded before it
	// existed, still has its text in description. Falling back keeps those
	// working rather than returning empty — degrading to the OLD behavior is
	// always better than degrading to nothing.
	if strings.TrimSpace(out.Transcription) == "" && strings.TrimSpace(out.Description) == "" {
		leftover := strings.TrimSpace(strings.Replace(s, m, "", 1))
		out.Transcription, out.Description = leftover, leftover
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
	return func(ctx context.Context, img PageImage, about RegionAbout) (RegionReading, error) {
		prev, prevCtx := o.Prompt, o.TraceCtx
		// Region identity for the trace, set alongside the prompt and restored
		// with it, so there is no path where one is set and the other is not.
		if about.Doc != "" || about.RegionID != "" {
			o.TraceCtx = map[string]any{
				"doc": about.Doc, "page": about.Page, "region": about.RegionID,
				"depth": about.Depth, "tokens_per_sq_in": about.TokensPerSqIn,
			}
			if about.Grid != "" {
				o.TraceCtx["grid"] = about.Grid
			}
			if about.Escalation != "" {
				o.TraceCtx["turn"] = "escalation"
			}
		}
		// The root is asked to account for the whole sheet; a crop is asked to
		// transcribe. Same walk, different question, decided by where we are in it.
		switch {
		case about.Escalation != "":
			// Turn 3 asks for a decision, not a reading — a different question
			// entirely, so it REPLACES the prompt rather than appending to it.
			o.Prompt = EscalatePrompt(about.Escalation)
		case about.Depth == 0 && about.FitsWhole:
			// Nothing will descend, so nothing needs salience: ask for the
			// transcription and only that.
			o.Prompt = Prompt(PromptPlain)
		case about.Depth == 0:
			o.Prompt = Prompt(PromptRoot, WithHint(hint), WithDamage(about.Damage), WithGrid(about.Grid))
		default:
			o.Prompt = Prompt(PromptCrop, WithHint(hint), WithDamage(about.Damage), WithGrid(about.Grid))
		}
		// The corpus hint goes on LAST and on every kind, escalation included:
		// it is context about the collection rather than a question, so it does
		// not compete with the one being asked, and a turn that decides whether
		// a page is upside down still benefits from knowing what the page is.
		o.Prompt += HintBlock(o.Collection)
		defer func() { o.Prompt, o.TraceCtx = prev, prevCtx }()
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
// fitsWhole reports whether every pixel of a region reaches the model in ONE
// canvas at this resolution — the arithmetic already in dpi.go, applied to the
// region's own area rather than the page's.
func (rr *RegionReader) fitsWhole(reg *Region) bool {
	areaSqIn := reg.BBox.W * rr.PageWIn * reg.BBox.H * rr.PageHIn
	if areaSqIn <= 0 || rr.DPI <= 0 {
		return false // unknown: keep the existing behaviour
	}
	return TilesNeeded(areaSqIn, rr.DPI, DefaultMaxImageTokens) <= 1
}

func (rr *RegionReader) tileRegion(reg *Region) []descent {
	if reg.TokensPerSqIn <= 0 {
		return nil
	}
	// How much more detail each tile needs than the region got.
	want := letterTokensPerSqIn / reg.TokensPerSqIn
	if want <= 1 {
		return nil
	}
	wIn, hIn := reg.BBox.W*rr.PageWIn, reg.BBox.H*rr.PageHIn
	cols, rows := gridFor(want, wIn, hIn)
	if wIn/float64(cols) < rr.MinRegionIn || hIn/float64(rows) < rr.MinRegionIn {
		return nil // already small enough that another cut buys no resolution
	}

	var out []descent
	tw, th := reg.BBox.W/float64(cols), reg.BBox.H/float64(rows)
	for row := range rows {
		for col := range cols {
			t := Rect{
				X: reg.BBox.X + float64(col)*tw,
				Y: reg.BBox.Y + float64(row)*th,
				W: tw, H: th,
			}.paddedIn(descentPadIn, rr.PageWIn, rr.PageHIn)
			out = append(out, descent{bbox: t, rotation: reg.Rotation, kind: "drawing",
				computed: true,
				grid:     fmt.Sprintf("row %d of %d, column %d of %d", row+1, rows, col+1, cols)})
		}
	}
	return out
}

// gridFor picks columns and rows so each TILE comes out roughly square, rather
// than cutting every sheet into an n x n grid whatever its shape.
//
// A square grid is only right for a square page. The shapes that actually turn
// up are not: a 66 x 36 inch map wants columns, a single tall column of
// newsprint wants rows, and a two-page spread is wider than it is tall by
// construction. Cutting a 2:1 sheet 4x4 makes sixteen tiles that are each 2:1 —
// every one of them still the wrong shape, and each seam falling wherever the
// arithmetic put it.
//
// So: spend the tile budget along the LONG axis first. want is the resolution
// deficit, i.e. how many letter-page-equivalents of detail are missing, and the
// area of the grid has to cover it; distributing that by aspect gives tiles that
// are square-ish whatever the sheet is.
//
// Capped at 6 per axis for the same reason the count was capped before: the
// point is to make the sheet legible, not to spend the budget. 6x6 is 36 calls.
func gridFor(want, wIn, hIn float64) (cols, rows int) {
	if wIn <= 0 || hIn <= 0 {
		n := min(max(int(math.Ceil(math.Sqrt(want))), 2), 6)
		return n, n
	}
	aspect := wIn / hIn
	// cols*rows >= want, with cols/rows == aspect makes each tile square.
	c := math.Sqrt(want * aspect)
	r := math.Sqrt(want / aspect)
	cols = min(max(int(math.Ceil(c)), 1), 6)
	rows = min(max(int(math.Ceil(r)), 1), 6)
	// Never a 1x1 "grid": that is the region itself and the cycle detector would
	// refuse it after a wasted render.
	if cols*rows < 2 {
		if aspect >= 1 {
			cols = 2
		} else {
			rows = 2
		}
	}
	return cols, rows
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
func (rr *RegionReader) wantsEscalation(parent, child *Region) bool {
	if rr.escalations >= rr.MaxEscalations || rr.calls >= rr.MaxCalls {
		return false
	}
	// A COMPUTED TILE DOES NOT ESCALATE. Its frame came from arithmetic that
	// covers the sheet exactly once — there is no "the box was wrong" hypothesis
	// for a parent to test, and a re-pick would punch a hole in a partition.
	// Escalation exists for a box a MODEL proposed and may have proposed badly.
	//
	// Measured 2026-08-07 on olmOCR-bench old_scans/1.pdf: every escalation came
	// from a grid cell. The root was re-read four times at 23431 tokens each, at
	// 6 tokens/in² against a readable 39 — five of twelve calls, 42% of the
	// budget, spent on a view that could not read anything. The walk then ran out
	// with 7 of 9 tiles read: the bottom-right of the page was never looked at,
	// which is most of why this descent scored WORSE than one plain read of the
	// same page (38.9% against 54.5%).
	if child.Computed {
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
			Escalation: escalationQuestion(parent, child),
			Doc:        rr.Doc, Page: parent.Page, RegionID: regionLabel(parent.ID),
			TokensPerSqIn: parent.TokensPerSqIn})
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

// canTile reports whether this region would be cut if it were read — used to
// decide whether a failed read is recoverable.
func (rr *RegionReader) canTile(reg *Region) bool {
	return rr.Tile && reg.hasFlag(FlagLowResolution) && len(rr.tileRegion(reg)) > 0
}

// withTiles prepends the computed grid to a region's descents when the region is
// under-resolved. See the commentary at its call site for why the gate is
// arithmetic alone.
// clusterRegions turns measured label clusters into descents. A cluster is
// bounded by whitespace, so a cut between clusters cannot sever a word — which a
// geometric grid does routinely. nil when nothing usable was found, and the
// caller falls back to the grid.
func (rr *RegionReader) clusterRegions(reg *Region) []descent {
	var out []descent
	for _, c := range reg.Clusters {
		// A cluster covering nearly the whole region IS the region: descending
		// re-renders the same crop and the cycle detector refuses it. Same rule
		// gridFor keeps as "never a 1x1 grid".
		if c.area() > 0.85 {
			continue
		}
		box := reg.BBox.within(c).paddedIn(descentPadIn, rr.PageWIn, rr.PageHIn)
		if wIn, hIn := box.W*rr.PageWIn, box.H*rr.PageHIn; wIn < rr.MinRegionIn && hIn < rr.MinRegionIn {
			continue
		}
		out = append(out, descent{bbox: box, rotation: reg.Rotation,
			kind: "cluster", computed: true})
	}
	return out
}

func (rr *RegionReader) withTiles(reg *Region, descents []descent) []descent {
	if !rr.Tile || !reg.hasFlag(FlagLowResolution) {
		return descents
	}
	// MEASURED: the grid wins. Segmentation was wired in and scored against the
	// geometric grid on the bench's E-size survey three times — 12/17, 13/17, and
	// (once the root-prompt and render-DPI confounds were gone, so this one is
	// the honest comparison) 5/6 against the grid's 6/6, from 23 crops instead of
	// 12 and 107KB of text instead of 19KB. It read MORE and found LESS: cutting
	// along ink boundaries splits a survey's callouts from the geometry they
	// annotate, and a label read without its drawing is a number without a
	// referent. So clusters are OPT-IN, and the component and its tests stay for
	// the corpus that does want them — dense label sheets with no through-lines.
	tiles := rr.tileRegion(reg)
	if rr.Segment {
		if cs := rr.clusterRegions(reg); len(cs) >= 2 {
			tiles = cs
		}
	}
	if len(tiles) == 0 {
		return descents
	}
	descents = append(tiles, descents...)
	if len(descents) > rr.MaxChildren {
		// The cap exists to stop a model naming forty overlapping blocks. Tiles
		// are a partition and are not that, so they raise it rather than being
		// truncated by it — cutting them would leave part of the drawing unread
		// while reporting success.
		descents = descents[:len(tiles)]
	}
	return descents
}

// tracef writes one indented trace line per turn when tracing is on.
func (rr *RegionReader) tracef(depth int, format string, a ...any) {
	if rr.Trace == nil {
		return
	}
	fmt.Fprintf(rr.Trace, "  regions | %s%s\n",
		strings.Repeat("  ", depth), fmt.Sprintf(format, a...))
}

func regionLabel(s string) string {
	if s == "" {
		return "(unnumbered)"
	}
	return s
}
