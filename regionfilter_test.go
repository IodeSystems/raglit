package raglit

import (
	"context"
	"image"
	"image/color"
	"testing"
)

// inkPage draws hairline strokes on white: something with edges, so blurring it
// has an effect the metrics can see.
func inkPage(w, h int) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	for y := 4; y < h-4; y += 6 {
		for x := 4; x < w-4; x++ {
			g.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	return g
}

// The measurement exists because the model cannot make it: shown a crop blurred
// at sigma 1.6 the vision model answered "skew" with 0.9 confidence. Laplacian
// variance separated the same pair by two orders of magnitude.
func TestBlurIsMeasuredWhereTheModelCannotSeeIt(t *testing.T) {
	sharp := inkPage(200, 200)
	blurred := gaussianBlurGray(sharp, 2.0)

	sv, bv := LaplacianVariance(sharp), LaplacianVariance(blurred)
	if bv >= sv {
		t.Fatalf("blurring did not reduce edge energy: sharp %.1f, blurred %.1f", sv, bv)
	}
	if got := DamageOf(sharp); len(got) != 0 {
		t.Errorf("a sharp page was flagged as damaged: %v (lapvar %.1f)", got, sv)
	}
	blurFlagged := false
	for _, f := range DamageOf(blurred) {
		if f == FlagBlurred {
			blurFlagged = true
		}
	}
	if !blurFlagged {
		t.Errorf("a blurred page was not flagged (lapvar %.1f, threshold %.0f)", bv, blurredLapVar)
	}
}

// A photocopy of a photocopy: the ink never reaches black and the paper never
// reaches white. Measured as the 1-99 percentile spread so one stray speck does
// not report a full range.
func TestFadeIsMeasuredAsACrushedRange(t *testing.T) {
	full := inkPage(120, 120)
	faded := image.NewGray(full.Bounds())
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			faded.SetGray(x, y, color.Gray{Y: uint8(100 + int(full.GrayAt(x, y).Y)*40/255)})
		}
	}
	if r := DynamicRange(full); r < 200 {
		t.Fatalf("the undamaged fixture is not full-range: %d", r)
	}
	if r := DynamicRange(faded); r >= fadedRangeSpan {
		t.Fatalf("fading did not crush the range: %d", r)
	}
	fadeFlagged := false
	for _, f := range DamageOf(faded) {
		if f == FlagFaded {
			fadeFlagged = true
		}
	}
	if !fadeFlagged {
		t.Errorf("a faded page was not flagged: %v", DamageOf(faded))
	}
}

// Contrast is the repair with the most to gain — measured, a faded crop read
// none of five facts unfiltered and three of five with it.
func TestContrastReopensACrushedRange(t *testing.T) {
	// Antialiased strokes on textured paper, not a bitmap. CLAHE equalises a
	// HISTOGRAM, so a fixture with two occupied levels is not a document and does
	// not measure the thing this filter was chosen for.
	src := gaussianBlurGray(inkPage(160, 160), 0.8)
	faded := image.NewGray(src.Bounds())
	for y := 0; y < 160; y++ {
		for x := 0; x < 160; x++ {
			grain := (x*7 + y*13) % 11 // paper, deterministic
			faded.SetGray(x, y, color.Gray{Y: uint8(100 + int(src.GrayAt(x, y).Y)*40/255 + grain)})
		}
	}
	out := ApplyRegionFilter(faded, FilterContrast)
	if before, after := DynamicRange(faded), DynamicRange(out); after <= before {
		t.Errorf("contrast did not widen the range: %d -> %d", before, after)
	}
	if b := out.Bounds(); b.Dx() != 160 || b.Dy() != 160 {
		t.Errorf("the filter changed the geometry: %v", b)
	}
}

func TestSharpenRaisesEdgeEnergyWithoutResizing(t *testing.T) {
	blurred := gaussianBlurGray(inkPage(160, 160), 2.0)
	out := ApplyRegionFilter(blurred, FilterSharpen)
	if before, after := LaplacianVariance(blurred), LaplacianVariance(out); after <= before {
		t.Errorf("sharpen did not raise edge energy: %.1f -> %.1f", before, after)
	}
	if b := out.Bounds(); b.Dx() != 160 || b.Dy() != 160 {
		t.Errorf("the filter changed the geometry: %v", b)
	}
}

// The filter is part of the render, not a display setting. A human attesting a
// quotation has to be shown the image the words came from, and if a repair was
// applied then THAT is the image.
func TestAFilteredRegionOnlyReproducesWithItsFilter(t *testing.T) {
	page := inkPage(400, 400)
	reg := &Region{Page: 1, BBox: Rect{0, 0, 1, 1}, Filter: FilterContrast}
	img, err := renderRegion(page, reg.BBox, reg.Rotation, reg.Filter)
	if err != nil {
		t.Fatal(err)
	}
	reg.SHA256 = imageSHA(img)

	again, err := RerenderRegion(page, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRegionRender(reg, again); err != nil {
		t.Errorf("a filtered region did not reproduce: %v", err)
	}
	unfiltered, err := renderRegion(page, reg.BBox, reg.Rotation, FilterNone)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRegionRender(reg, unfiltered); err == nil {
		t.Error("the unfiltered crop verified against a filtered region's digest")
	}
}

// A repair is asked for on the SAME pixels, which makes it a transform and not a
// descent — the same routing rule rotation already gets.
func TestAFilterOnTheSameAreaIsATransform(t *testing.T) {
	page := blankPage(400, 540)
	ask, calls := scriptedAsk(
		RegionReading{Description: "a faint page", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 1, H: 1, Filter: FilterContrast, Reason: "the scan is faint"},
		}},
		RegionReading{Description: "PARCEL A: THE EASTERLY 25 FEET OF LOT 1, BLOCK 10"},
	)
	root, err := surveyReader(ask).Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if root.Filter != FilterContrast {
		t.Errorf("the repair was not adopted: filter=%q flags=%v", root.Filter, root.Flags)
	}
	if *calls < 2 {
		t.Errorf("the transform was never read: %d calls", *calls)
	}
	img, err := RerenderRegion(page, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRegionRender(root, img); err != nil {
		t.Errorf("the adopted repair does not reproduce: %v", err)
	}
}

// A model naming a repair this package cannot apply must not look like a model
// asking for nothing — the proposal is neutralised, and a same-area same-render
// proposal is then dropped by the existing rule rather than costing a call.
func TestAnInventedFilterIsRefusedRatherThanApplied(t *testing.T) {
	if ValidRegionFilter("denoise") {
		t.Error("denoise is not implemented and must not validate")
	}
	for _, f := range []RegionFilter{FilterNone, FilterContrast, FilterSharpen} {
		if !ValidRegionFilter(f) {
			t.Errorf("%q is implemented and must validate", f)
		}
	}
	page := blankPage(400, 540)
	ask, calls := scriptedAsk(
		RegionReading{Description: "a page", Regions: []RegionProposal{
			{X: 0, Y: 0, W: 1, H: 1, Filter: "denoise", Reason: "invented"},
		}},
		RegionReading{Description: "should never be reached"},
	)
	root, err := surveyReader(ask).Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if root.Filter != FilterNone {
		t.Errorf("an unimplemented filter was applied: %q", root.Filter)
	}
	if *calls != 1 {
		t.Errorf("an invented filter cost a model call: %d", *calls)
	}
}

// The measurement is passed to the model rather than acted on behind it: the
// number is what NOTICES, and the model is what decides, because it is the one
// that can see whether this is a faded fax or a drawing that is mostly paper.
func TestTheMeasuredDamageIsPutToTheModel(t *testing.T) {
	var sawDamage [][]string
	ask := func(_ context.Context, _ PageImage, _ int, damage []string) (RegionReading, error) {
		sawDamage = append(sawDamage, damage)
		return RegionReading{Description: "read"}, nil
	}
	// A uniform grey page: no edges, no range — damaged on both counts.
	flat := image.NewGray(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			flat.SetGray(x, y, color.Gray{Y: 140})
		}
	}
	if _, err := surveyReader(ask).Read(context.Background(), flat, 1); err != nil {
		t.Fatal(err)
	}
	if len(sawDamage) == 0 {
		t.Fatal("the model was never asked")
	}
	if len(sawDamage[0]) == 0 {
		t.Errorf("the model was asked without being told what was measured: %v", sawDamage[0])
	}
	if s := damageSuffix(sawDamage[0]); s == "" {
		t.Error("measured damage produced no instruction to the model")
	}
	if damageSuffix(nil) != "" {
		t.Error("an undamaged region must not carry a repair instruction")
	}
}
