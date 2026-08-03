package raglit

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
)

func nearly(a, b float64) bool { return a-b < 0.002 && b-a < 0.002 }

// blankPage is a white sheet; renderRegion only needs something to crop.
func blankPage(w, h int) image.Image {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Set(x, y, color.White)
		}
	}
	return im
}

// scriptedAsk replays readings in order and records what it was shown.
func scriptedAsk(readings ...RegionReading) (func(context.Context, PageImage, int) (RegionReading, error), *int) {
	n := 0
	f := func(_ context.Context, _ PageImage, _ int) (RegionReading, error) {
		r := RegionReading{}
		if n < len(readings) {
			r = readings[n]
		}
		n++
		return r, nil
	}
	return f, &n
}

func surveyReader(ask func(context.Context, PageImage, int) (RegionReading, error)) *RegionReader {
	// MaxDepth is explicit here: descent is opt-in now, and these tests are ABOUT
	// descent, so they have to ask for it like any caller would.
	return &RegionReader{Ask: ask, PageWIn: 27.0, PageHIn: 36.7, DPI: 200, MaxDepth: 3}
}

// The measured failure: an E-size sheet read whole gets ~4 tokens per square
// inch against a letter page's 39. That is knowable before the model is called,
// and on its own it means the transcription cannot be trusted.
func TestRootOfAnOversizeSheetIsFlaggedLowResolutionBeforeAnyCall(t *testing.T) {
	ask, calls := scriptedAsk(RegionReading{Description: "a survey"})
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(400, 540), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !root.hasFlag(FlagLowResolution) {
		t.Errorf("an E-size sheet read whole must be flagged low-resolution, got %v", root.Flags)
	}
	if root.TokensPerSqIn > 10 {
		t.Errorf("expected a few tokens per sq in, got %.1f", root.TokensPerSqIn)
	}
	if *calls != 1 {
		t.Errorf("want one call, got %d", *calls)
	}
}

// A letter page is the baseline and must NOT be flagged.
func TestLetterPageIsNotFlaggedLowResolution(t *testing.T) {
	ask, _ := scriptedAsk(RegionReading{Description: "a deed"})
	rr := &RegionReader{Ask: ask, PageWIn: 8.5, PageHIn: 11, DPI: 200, MaxDepth: 3}
	root, err := rr.Read(context.Background(), blankPage(1700, 2200), 1)
	if err != nil {
		t.Fatal(err)
	}
	if root.hasFlag(FlagLowResolution) {
		t.Errorf("a letter page at 200dpi is the baseline; it was flagged: %v (%.0f t/in²)",
			root.Flags, root.TokensPerSqIn)
	}
}

// Descending into a quarter of the sheet must actually buy resolution — that is
// the whole mechanism.
func TestDescentRaisesResolution(t *testing.T) {
	ask, _ := scriptedAsk(
		RegionReading{Description: "whole sheet", Kind: "drawing", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 0.25, H: 0.25, Kind: "text-block"},
		}},
		RegionReading{Description: "THE EASTERLY 25 FEET..."},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(800, 1080), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("want one child, got %d", len(root.Children))
	}
	child := root.Children[0]
	if child.TokensPerSqIn <= root.TokensPerSqIn {
		t.Errorf("descent bought no resolution: root %.1f, child %.1f",
			root.TokensPerSqIn, child.TokensPerSqIn)
	}
	if !strings.Contains(child.Text, "EASTERLY") {
		t.Errorf("the child's transcription was lost: %q", child.Text)
	}
}

// The observed hazard: the model proposes, as a sub-region, the region it was
// just given. Routing is by GEOMETRY, so a full-area proposal at the same
// rotation is neither a descent nor a transform and is dropped.
func TestAFullAreaProposalAtTheSameRotationIsRefused(t *testing.T) {
	ask, calls := scriptedAsk(
		RegionReading{Description: "whole sheet", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 1, H: 1, Rotation: 0, Reason: "look again"},
		}},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(400, 540), 1)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Errorf("re-reading the same region was allowed: %d calls", *calls)
	}
	if !root.hasFlag(FlagExhausted) {
		t.Errorf("nothing actionable was proposed, so the region is exhausted: %v", root.Flags)
	}
}

// A full-area proposal at a DIFFERENT rotation is legitimate — a text block
// running sideways genuinely needs re-rotating — and is routed as a transform,
// not as a child.
func TestAFullAreaProposalAtANewRotationIsATransform(t *testing.T) {
	ask, calls := scriptedAsk(
		RegionReading{Description: "sideways text", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 1, H: 1, Rotation: 90, Reason: "text runs sideways"},
		}},
		RegionReading{Description: "PARCEL A: THE EASTERLY 25 FEET"},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(400, 540), 1)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("want the rotated re-read, got %d calls", *calls)
	}
	if len(root.Children) != 0 {
		t.Errorf("a transform must not become a child: %d children", len(root.Children))
	}
	if root.Rotation != 90 {
		t.Errorf("the better rotation was not kept: %d", root.Rotation)
	}
	if !strings.Contains(root.Text, "EASTERLY") {
		t.Errorf("the rotated reading was not kept: %q", root.Text)
	}
}

// The cycle detector: the page cache keys on image bytes, and rotating a square
// region by 360 returns the original SHA. A transform that renders to bytes
// already seen is refused rather than recursed into.
func TestATransformRenderingToSeenBytesIsRefused(t *testing.T) {
	// Every reading asks to be re-shown at 0 degrees, forever.
	ask := func(_ context.Context, _ PageImage, _ int) (RegionReading, error) {
		return RegionReading{Description: "same again", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 1, H: 1, Rotation: 360, Reason: "again"},
		}}, nil
	}
	rr := surveyReader(ask)
	rr.MaxCalls = 50
	root, err := rr.Read(context.Background(), blankPage(400, 540), 1)
	if err != nil {
		t.Fatal(err)
	}
	if rr.calls > 3 {
		t.Errorf("a cycling proposal was followed %d times", rr.calls)
	}
	if !root.hasFlag(FlagCycled) {
		t.Errorf("the cycle was not recorded: %v", root.Flags)
	}
}

// A model that proposes children forever must still terminate, and the record
// must say it stopped for budget rather than because it finished.
func TestDescentIsBoundedByDepthAndCalls(t *testing.T) {
	ask := func(_ context.Context, _ PageImage, _ int) (RegionReading, error) {
		return RegionReading{Description: "more", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 0.5, H: 0.5}, {X: 0.5, Y: 0, W: 0.5, H: 0.5},
			{X: 0, Y: 0.5, W: 0.5, H: 0.5}, {X: 0.5, Y: 0.5, W: 0.5, H: 0.5},
		}}, nil
	}
	rr := surveyReader(ask)
	rr.MaxDepth, rr.MaxCalls = 2, 12
	root, err := rr.Read(context.Background(), blankPage(800, 1080), 1)
	if err != nil {
		t.Fatal(err)
	}
	if rr.calls > 12 {
		t.Errorf("the call budget was exceeded: %d", rr.calls)
	}
	var deepest int
	for _, n := range root.Flatten() {
		if n.Depth > deepest {
			deepest = n.Depth
		}
	}
	if deepest > 2 {
		t.Errorf("depth %d exceeds MaxDepth 2", deepest)
	}
	var sawBudget bool
	for _, n := range root.Flatten() {
		if n.hasFlag(FlagBudget) {
			sawBudget = true
		}
	}
	if !sawBudget {
		t.Error("stopping for budget must be recorded, not left to look like completion")
	}
}

// A region too small to gain from another look is not descended into: the pixels
// are already all there.
func TestTinyProposalsAreNotDescendedInto(t *testing.T) {
	ask, calls := scriptedAsk(
		RegionReading{Description: "sheet", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 0.01, H: 0.01, Reason: "a monument marker"},
		}},
	)
	rr := surveyReader(ask)
	rr.MinRegionIn = 1.0
	if _, err := rr.Read(context.Background(), blankPage(400, 540), 1); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Errorf("descended into a sub-inch region: %d calls", *calls)
	}
}

// Coordinates come back in the region's own frame and must be lifted to the
// page, or a grandchild points at the wrong part of the sheet.
func TestChildCoordinatesAreLiftedIntoPageSpace(t *testing.T) {
	ask, _ := scriptedAsk(
		RegionReading{Description: "sheet", Regions: []RegionProposal{{X: 0.5, Y: 0.5, W: 0.5, H: 0.5}}},
		RegionReading{Description: "quadrant", Regions: []RegionProposal{{X: 0.5, Y: 0.5, W: 0.5, H: 0.5}}},
		RegionReading{Description: "corner"},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(800, 1080), 1)
	if err != nil {
		t.Fatal(err)
	}
	// Children are PADDED by a constant DISTANCE — descentPadIn inches — not by a
	// fraction of the region. Expressed from the page width rather than as a
	// literal, so the assertion says what the rule is instead of restating one
	// arithmetic result of it.
	child := root.Children[0]
	wantX := 0.5 - descentPadIn/27.0
	if !nearly(child.BBox.X, wantX) {
		t.Fatalf("child bbox not lifted+padded as expected: got %+v, want X=%.6f", child.BBox, wantX)
	}
	if len(child.Children) != 1 {
		t.Fatalf("want a grandchild, got %d", len(child.Children))
	}
	// The grandchild's 0.5,0.5,0.5,0.5 is expressed in the CHILD's frame, so it
	// must land in the child's second half, not the page's.
	g := child.Children[0].BBox
	if g.X < child.BBox.X+child.BBox.W*0.4 {
		t.Errorf("grandchild not lifted into page space: %+v within child %+v", g, child.BBox)
	}
	if g.X+g.W > 1.0001 {
		t.Errorf("grandchild escaped the page: %+v", g)
	}
}

// A malformed reply must not lose the transcription — that is the part that
// matters, and JSON is only the wrapper.
func TestParseRegionReadingKeepsTextWhenJSONIsUnusable(t *testing.T) {
	got := ParseRegionReading("I could not produce JSON. PARCEL A: THE EASTERLY 25 FEET")
	if !strings.Contains(got.Description, "EASTERLY") {
		t.Errorf("the reading was discarded with the JSON: %q", got.Description)
	}
	ok := ParseRegionReading("here you go:\n```json\n{\"description\":\"D\",\"kind\":\"table\",\"regions\":[]}\n```")
	if ok.Description != "D" || ok.Kind != "table" {
		t.Errorf("a fenced object was not parsed: %+v", ok)
	}
}

// Leaves are what get embedded; interior nodes stay searchable through their own
// overview text, which is what makes a bad region set cost detail not coverage.
func TestLeavesAndFlattenAgree(t *testing.T) {
	ask, _ := scriptedAsk(
		RegionReading{Description: "sheet", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 0.4, H: 0.4}, {X: 0.5, Y: 0.5, W: 0.4, H: 0.4},
		}},
		RegionReading{Description: "block one"},
		RegionReading{Description: "block two"},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(800, 1080), 1)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(root.Flatten()); n != 3 {
		t.Errorf("want 3 regions, got %d", n)
	}
	if n := len(root.Leaves()); n != 2 {
		t.Errorf("want 2 leaves, got %d", n)
	}
	if root.Text == "" {
		t.Error("the root's own description must survive; it is the coverage guarantee")
	}
}

// A model asked for a text block returns the block, not the block plus a
// margin. The first verification tile cut mid-word ("REPLAT OF BLO"), so
// descents are grown to overlap their neighbours.
func TestDescentsArePaddedToAvoidCuttingText(t *testing.T) {
	ask, _ := scriptedAsk(
		RegionReading{Description: "sheet", Regions: []RegionProposal{
			{X: 0.25, Y: 0.25, W: 0.25, H: 0.25, Kind: "text-block"},
		}},
		RegionReading{Description: "block"},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(800, 1080), 1)
	if err != nil {
		t.Fatal(err)
	}
	got := root.Children[0].BBox
	if got.W <= 0.25 || got.X >= 0.25 {
		t.Errorf("descent was not padded: %+v (proposal was x=.25 w=.25)", got)
	}
}

// The model names the same block twice under different reasons, and each
// proposal costs a model call.
func TestOverlappingProposalsAreDeduped(t *testing.T) {
	ask, calls := scriptedAsk(
		RegionReading{Description: "sheet", Regions: []RegionProposal{
			{X: 0.1, Y: 0.1, W: 0.3, H: 0.3, Reason: "text block"},
			{X: 0.11, Y: 0.11, W: 0.3, H: 0.3, Reason: "the legal description"},
		}},
		RegionReading{Description: "block"},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(800, 1080), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 {
		t.Errorf("near-identical proposals were both read: %d children", len(root.Children))
	}
	if *calls != 2 {
		t.Errorf("want 2 calls (root + one block), got %d", *calls)
	}
}

// --- what the model actually saw ---------------------------------------------

// A human asked to attest that a document says what a fact quotes must be shown
// the image the words came from. That is only possible if every region records
// what it was rendered from — and the descent already computed the digest, for
// the cycle detector, and threw it away.
func TestEveryRegionRecordsWhatItWasReadFrom(t *testing.T) {
	ask, _ := scriptedAsk(
		RegionReading{Description: "sheet", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 0.4, H: 0.4}, {X: 0.5, Y: 0.5, W: 0.4, H: 0.4},
		}},
		RegionReading{Description: "block one"},
		RegionReading{Description: "block two"},
	)
	rr := surveyReader(ask)
	root, err := rr.Read(context.Background(), blankPage(800, 1080), 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p3", "p3.0", "p3.1"}
	var got []string
	for _, n := range root.Flatten() {
		got = append(got, n.ID)
		if n.SHA256 == "" {
			t.Errorf("region %s recorded no digest; nothing can attest to its text", n.ID)
		}
		if n.DPI != 200 {
			t.Errorf("region %s recorded dpi %d, want the 200 it was rendered at", n.ID, n.DPI)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ids %v, want %v — the id is the page and the path down to the region", got, want)
	}
	if root.FindByID("p3.1") == nil {
		t.Error("a recorded id must resolve back to its region")
	}
	if root.FindByID("p3.9") != nil {
		t.Error("an id that names nothing must not resolve")
	}
}

// The core deliverable: re-rendering a region reproduces the bytes it was read
// from, so what a human is shown is the artifact and not something that merely
// has the same coordinates.
func TestRerenderReproducesTheBytesTheRegionWasReadFrom(t *testing.T) {
	page := blankPage(800, 1080)
	ask, _ := scriptedAsk(
		RegionReading{Description: "sheet", Regions: []RegionProposal{
			{X: 0.1, Y: 0.1, W: 0.3, H: 0.3, Rotation: 90},
		}},
		RegionReading{Description: "PARCEL A"},
	)
	root, err := surveyReader(ask).Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range root.Flatten() {
		img, err := RerenderRegion(page, n)
		if err != nil {
			t.Fatalf("region %s: %v", n.ID, err)
		}
		if err := VerifyRegionRender(n, img); err != nil {
			t.Errorf("region %s did not reproduce: %v", n.ID, err)
		}
	}
	// And a crop taken at the WRONG rotation must be reported, not shown.
	wrong := *root.Children[0]
	wrong.Rotation = 180
	img, err := RerenderRegion(page, &wrong)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRegionRender(root.Children[0], img); err == nil {
		t.Error("a crop at a different rotation passed verification")
	}
}

// A page too large for the model's context is re-rendered SMALLER mid-call, and
// nothing about the crop geometry records that. Unrecorded, the diagnostic
// question — could this have been read at all — has no answer.
func TestTheContextDownscalesAreRecordedAndReplayable(t *testing.T) {
	page := blankPage(400, 540)
	ask := func(_ context.Context, _ PageImage, _ int) (RegionReading, error) {
		return RegionReading{Description: "shrunk twice", Downscales: 2}, nil
	}
	root, err := surveyReader(ask).Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if root.Downscales != 2 {
		t.Fatalf("the shrink was not recorded: %d", root.Downscales)
	}
	crop, err := RerenderRegion(page, root)
	if err != nil {
		t.Fatal(err)
	}
	// The digest is taken before the call, so the CROP is what verifies.
	if err := VerifyRegionRender(root, crop); err != nil {
		t.Errorf("the crop must still reproduce exactly: %v", err)
	}
	seen, err := RerenderRegionAsSeen(page, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) >= len(crop) {
		t.Error("the replay did not shrink; it is a sharper image than the model was given")
	}
	again, err := RerenderRegionAsSeen(page, root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(seen, again) {
		t.Error("replaying the downscales is not deterministic")
	}
}

// The transform keeps the better reading. Keeping its TEXT without its digest
// would leave the region claiming its words were read off the image it replaced
// — a false attestation, reached without anyone editing anything.
func TestATransformCarriesItsRenderRecordOntoTheRegion(t *testing.T) {
	page := blankPage(400, 540)
	ask, _ := scriptedAsk(
		RegionReading{Description: "sideways", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 1, H: 1, Rotation: 90, Reason: "text runs sideways"},
		}},
		RegionReading{Description: "PARCEL A: THE EASTERLY 25 FEET"},
	)
	root, err := surveyReader(ask).Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if root.Rotation != 90 {
		t.Fatalf("the transform was not adopted: %d", root.Rotation)
	}
	upright, err := renderRegion(page, root.BBox, 0)
	if err != nil {
		t.Fatal(err)
	}
	if root.SHA256 == imageSHA(upright) {
		t.Error("the region kept the rotated text but the unrotated digest")
	}
	img, err := RerenderRegion(page, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRegionRender(root, img); err != nil {
		t.Errorf("the adopted transform does not reproduce: %v", err)
	}
}

// The root is asked to ACCOUNT for the sheet; a crop is asked to transcribe.
// Same walk, different question, and the difference is what stops a plan sheet
// coming back described as the one inset somebody's eye landed on.
func TestTheRootIsAskedADifferentQuestionThanACrop(t *testing.T) {
	var asked []string
	rr := &RegionReader{
		PageWIn: 27, PageHIn: 36.7, DPI: 200, MaxDepth: 1,
		Ask: func(_ context.Context, _ PageImage, depth int) (RegionReading, error) {
			if depth == 0 {
				asked = append(asked, "root")
				return RegionReading{
					Description: "the whole sheet",
					Regions:     []RegionProposal{{X: 0.1, Y: 0.1, W: 0.2, H: 0.2, Kind: "title-block"}},
				}, nil
			}
			asked = append(asked, "crop")
			return RegionReading{Description: "small print"}, nil
		},
	}
	if _, err := rr.Read(context.Background(), blankPage(800, 1000), 1); err != nil {
		t.Fatal(err)
	}
	if len(asked) < 2 || asked[0] != "root" || asked[1] != "crop" {
		t.Fatalf("want the root asked first and then a crop, got %v", asked)
	}
}

// Descent is opt-in. A page that is read and understood should not pay for
// crops nobody asked for — the root reading is written to stand on its own.
func TestDescentIsOptIn(t *testing.T) {
	calls := 0
	rr := &RegionReader{
		PageWIn: 27, PageHIn: 36.7, DPI: 200, // MaxDepth left at 0
		Ask: func(_ context.Context, _ PageImage, _ int) (RegionReading, error) {
			calls++
			return RegionReading{
				Description: "the whole sheet",
				Regions: []RegionProposal{
					{X: 0.1, Y: 0.1, W: 0.2, H: 0.2, Kind: "title-block"},
					{X: 0.5, Y: 0.5, W: 0.2, H: 0.2, Kind: "legend"},
				},
			}, nil
		},
	}
	root, err := rr.Read(context.Background(), blankPage(800, 1000), 1)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("want exactly one read with no depth asked for, got %d", calls)
	}
	if len(root.Children) != 0 {
		t.Errorf("want no descent, got %d child region(s)", len(root.Children))
	}
	if root.Text == "" {
		t.Error("the root must still carry its account of the sheet")
	}
}

// Measured 2026-08-03 on page 2 of the survey: read sideways the model does not
// lose text, it runs on — 9,316 characters with 89% of its lines duplicated,
// against 2,187 characters and 2% for the same region read upright. The rule
// this replaces was "more text wins", which prefers exactly the render that got
// two bearings wrong.
func TestALongerTransformThatIsMostlyRepetitionIsRefused(t *testing.T) {
	looped := "FOUND 1/2 RB/CAP SUMMIT 0.2 N OF CALC\n" +
		strings.Repeat("FOUND 5/8 RB/CAP MOWRER 0.4 S OF CALC\n", 40)
	upright := "EXISTING CORNERS\nA = FOUND 1/2 RB/CP SUMMIT\n" +
		"B = FOUND 1/2 RB/CAP SUMMIT 0.1 N AND 0.1 W OF CALCD POSITION\n" +
		"J = FOUND 1/2 RB/CAP SUMMIT NOT ACCEPTED S 31 05 E 0.4 FROM CALC\n"
	if len(looped) <= len(upright) {
		t.Fatalf("fixture is not the case under test: %d vs %d", len(looped), len(upright))
	}
	orig := &Region{Text: upright}
	alt := &Region{Text: looped}
	if transformHelped(orig, alt, "") {
		t.Errorf("a %d-char loop beat a %d-char reading (degenerate ratio %.2f vs %.2f)",
			len(looped), len(upright), degenerateRatio(looped), degenerateRatio(upright))
	}
	// And the other way round: escaping a loop is exactly what a transform is for.
	if !transformHelped(&Region{Text: looped}, &Region{Text: upright}, "") {
		t.Error("a transform that broke the loop was refused")
	}
}

// With nothing else to separate them the old rule still applies, corrected: more
// DISTINCT content, so a re-render that only repeats itself more cannot win.
func TestATransformWinsOnDistinctContentWhenNothingElseSeparatesThem(t *testing.T) {
	orig := &Region{Text: "sideways"}
	alt := &Region{Text: "PARCEL A: THE EASTERLY 25 FEET OF LOT 1\nBLOCK 10, MONTBORNE"}
	if !transformHelped(orig, alt, "") {
		t.Error("the legible re-render was refused")
	}
	padded := &Region{Text: "sideways\n" + strings.Repeat("sideways\n", 20)}
	if transformHelped(orig, padded, "") {
		t.Error("repeating the same line counted as more content")
	}
}

// The parent's account is the only description of a region not produced by the
// render under judgement, which is what makes it usable as a tiebreak.
func TestATransformIsJudgedAgainstWhatTheParentSaidIsThere(t *testing.T) {
	expect := "a monument table listing found rebar and caps with offsets from calculated position"
	about := &Region{Text: "FOUND 5/8 RB/CAP MOWRER 0.4 S OF CALCULATED POSITION\n" +
		"FOUND REBAR AND CAP LISSER PER PREVIOUS SURVEY\n"}
	elsewhere := &Region{Text: "NORTHERN PACIFIC RAILROAD RIGHT OF WAY\n" +
		"VACATED SHERMAN STREET\nAERIAL UTILITY CROSSING\nGRAVEL PARKING AREA\n"}
	if !transformHelped(elsewhere, about, expect) {
		t.Error("the render matching the parent's account lost")
	}
	if transformHelped(about, elsewhere, expect) {
		t.Error("a render about something else won")
	}
}

// A rotation applied the wrong way round does not garble the text — it returns a
// reading about a different part of the sheet. That is a claim about the
// TRANSFORM, and the region says so rather than adopting it silently.
func TestAChildThatSharesNothingWithItsParentFlagsTheTransform(t *testing.T) {
	page := blankPage(400, 540)
	ask, _ := scriptedAsk(
		RegionReading{
			Description: "a monument table listing found rebar and caps with offsets from calculated position",
			Regions: []RegionProposal{
				{X: 0.1, Y: 0.1, W: 0.3, H: 0.3, Rotation: 90, Reason: "the table"},
			}},
		RegionReading{Description: "NORTHERN PACIFIC RAILROAD RIGHT OF WAY VACATED SHERMAN STREET"},
	)
	root, err := surveyReader(ask).Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("expected one child, got %d", len(root.Children))
	}
	if !root.Children[0].hasFlag(FlagTransformSuspect) {
		t.Errorf("a child about something else was accepted unflagged: %v", root.Children[0].Flags)
	}
	if root.hasFlag(FlagTransformSuspect) {
		t.Error("the root has no parent to disagree with and must not be flagged")
	}
}

// The flag is about disagreement, not about a child saying MORE than its parent
// could see — which is the entire point of descending.
func TestAChildThatElaboratesOnItsParentIsNotFlagged(t *testing.T) {
	page := blankPage(400, 540)
	ask, _ := scriptedAsk(
		RegionReading{
			Description: "a monument table listing found rebar and caps with offsets from calculated position",
			Regions: []RegionProposal{
				{X: 0.1, Y: 0.1, W: 0.3, H: 0.3, Reason: "the table"},
			}},
		RegionReading{Description: "EXISTING CORNERS\n" +
			"A = a monument table listing found rebar and caps with offsets from calculated position\n" +
			"B = FOUND 1/2 RB/CAP SUMMIT 0.1 N AND 0.1 W OF CALCD POSITION\n"},
	)
	root, err := surveyReader(ask).Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if root.Children[0].hasFlag(FlagTransformSuspect) {
		t.Errorf("a child that covered its parent's account was flagged: %v", root.Children[0].Flags)
	}
}
