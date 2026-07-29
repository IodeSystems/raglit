package raglit

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
)

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
	return &RegionReader{Ask: ask, PageWIn: 27.0, PageHIn: 36.7, DPI: 200}
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
	rr := &RegionReader{Ask: ask, PageWIn: 8.5, PageHIn: 11, DPI: 200}
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
	child := root.Children[0]
	if child.BBox.X != 0.5 || child.BBox.W != 0.5 {
		t.Fatalf("child bbox wrong: %+v", child.BBox)
	}
	if len(child.Children) != 1 {
		t.Fatalf("want a grandchild, got %d", len(child.Children))
	}
	g := child.Children[0].BBox
	if g.X != 0.75 || g.W != 0.25 {
		t.Errorf("grandchild not lifted into page space: %+v (want x=0.75 w=0.25)", g)
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
