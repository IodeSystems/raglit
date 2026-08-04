package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// recordedSheet writes a page image and the region read of it, the way `raglit
// regions --write` would, and returns the document path.
//
// The read is driven by a scripted model so the test exercises the real descent
// — the digests, dpi and ids on the sidecar are the ones the walk produced, not
// values a test made up.
func recordedSheet(t *testing.T) (docPath string, root *raglit.Region) {
	t.Helper()
	dir := t.TempDir()
	docPath = filepath.Join(dir, "survey.png")

	// A sheet with something on it: a re-render that got the crop wrong on a
	// blank page would still match, so the page has to vary.
	im := image.NewRGBA(image.Rect(0, 0, 800, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 800; x++ {
			im.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 0xff, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	n := 0
	readings := []raglit.RegionReading{
		{Description: "A record of survey showing parcels A, B and C.", Kind: "drawing",
			Regions: []raglit.RegionProposal{{X: 0.1, Y: 0.1, W: 0.3, H: 0.25, Rotation: 90, Kind: "text-block"}}},
		{Description: "THAT LIES WESTERLY OF THE CENTERLINE OF SAID RIGHT-OF-WAY", Kind: "text-block"},
	}
	rr := &raglit.RegionReader{
		// Descent is opt-in; this test is about a CHILD region, so it asks.
		MaxDepth: 3,
		PageWIn:  27, PageHIn: 36.7, DPI: 200,
		Ask: func(context.Context, raglit.PageImage, raglit.RegionAbout) (raglit.RegionReading, error) {
			r := raglit.RegionReading{}
			if n < len(readings) {
				r = readings[n]
			}
			n++
			return r, nil
		},
	}
	// Decoded from the file, so the sidecar records the same rasterization the
	// `region` command will reproduce.
	f, err := os.Open(docPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	page, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	root, err = rr.Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	text, spans := raglit.RegionTranscript(root)
	b := page.Bounds()
	if _, err := raglit.WriteRegionDoc(docPath, raglit.RegionPage{
		Page: 1, WidthIn: 27, HeightIn: 36.7, DPI: 200,
		PxW: b.Dx(), PxH: b.Dy(), Root: root, Text: text, Spans: spans,
	}); err != nil {
		t.Fatal(err)
	}
	return docPath, root
}

// The core deliverable, end to end: given a region id, put back on disk the
// exact image that region's text was read from — same crop, same rotation, same
// dpi — and prove it is that image rather than one with the same coordinates.
func TestRegionCommandReproducesTheCropAPassageWasReadFrom(t *testing.T) {
	doc, root := recordedSheet(t)
	child := root.Children[0]
	out := filepath.Join(t.TempDir(), "crop.png")

	if err := runRegion([]string{"--strict", "--out", out, doc, child.ID}); err != nil {
		t.Fatalf("--strict re-render failed, so it did not reproduce: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := raglit.VerifyRegionRender(child, got); err != nil {
		t.Errorf("the written crop is not the recorded image: %v", err)
	}

	// It is the ROTATED crop, not the page: 90° turns a 0.3x0.25 box of an
	// 800x1080 sheet on its side, and the human has to see it upright.
	im, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	b := im.Bounds()
	if b.Dx() <= b.Dy() {
		t.Errorf("crop is %dx%d; a 90° rotation of a tall box should come back wide", b.Dx(), b.Dy())
	}
	if b.Dx() >= 800 {
		t.Errorf("crop is %d wide — that is the whole sheet, not the region", b.Dx())
	}
}

// A region id that names nothing, and a document with no read at all, both have
// to say so. Silently rendering the page instead would hand the human exactly
// the wrong artifact with no indication.
func TestRegionCommandRefusesWhatItCannotReproduce(t *testing.T) {
	doc, _ := recordedSheet(t)
	if err := runRegion([]string{"--out", filepath.Join(t.TempDir(), "x.png"), doc, "p1.9"}); err == nil {
		t.Error("an unknown region id was rendered anyway")
	}
	unread := filepath.Join(t.TempDir(), "deed.pdf")
	if err := os.WriteFile(unread, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runRegion([]string{"--list", unread})
	if err == nil {
		t.Fatal("a document with no recorded read must say so")
	}
	if !strings.Contains(err.Error(), "regions --write") {
		t.Errorf("the error should say how to record one: %v", err)
	}
}

// minimalPDF is a one-page letter sheet with a line of text on it. Hand-rolled
// because the point is to exercise the real rasterization path — pdfinfo for the
// physical size, pdftoppm for the pixels — and a PDF written by anything else
// would not make that any more real.
const minimalPDF = "%PDF-1.4\n" +
	"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]" +
	"/Resources<</Font<</F1 5 0 R>>>>/Contents 4 0 R>>endobj\n" +
	"4 0 obj<</Length 62>>stream\n" +
	"BT /F1 24 Tf 72 700 Td (THAT LIES WESTERLY OF THE) Tj ET\n" +
	"endstream endobj\n" +
	"5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\n" +
	"trailer<</Root 1 0 R>>\n"

// The PDF path is the one that matters — a survey is a PDF — and it is the one
// where reproducibility is not free: the crop comes off a rasterization produced
// by an external renderer.
//
// Measured, not assumed: pdftoppm at a fixed dpi is byte-identical run to run,
// so a re-render reproduces the digest. Across renderer VERSIONS it is not
// guaranteed, which is what the recorded page pixel size is for.
func TestRegionCommandReproducesACropOutOfARasterizedPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	dir := t.TempDir()
	doc := filepath.Join(dir, "deed.pdf")
	if err := os.WriteFile(doc, []byte(minimalPDF), 0o644); err != nil {
		t.Fatal(err)
	}

	wIn, hIn, err := pageSizeInches(doc, 1)
	if err != nil {
		t.Fatal(err)
	}
	if wIn < 8.4 || wIn > 8.6 {
		t.Fatalf("letter sheet measured %.2f x %.2f in", wIn, hIn)
	}
	page, err := renderPage(doc, 1, 200)
	if err != nil {
		t.Fatal(err)
	}

	n := 0
	rr := &raglit.RegionReader{
		// Descent is opt-in; this test is about a CHILD region, so it asks.
		MaxDepth: 3,
		PageWIn:  wIn, PageHIn: hIn, DPI: 200,
		Ask: func(context.Context, raglit.PageImage, raglit.RegionAbout) (raglit.RegionReading, error) {
			n++
			if n == 1 {
				return raglit.RegionReading{Description: "a deed", Kind: "text-block",
					Regions: []raglit.RegionProposal{{X: 0.05, Y: 0.05, W: 0.9, H: 0.2, Kind: "text-block"}}}, nil
			}
			return raglit.RegionReading{Description: "THAT LIES WESTERLY OF THE"}, nil
		},
	}
	root, err := rr.Read(context.Background(), page, 1)
	if err != nil {
		t.Fatal(err)
	}
	text, spans := raglit.RegionTranscript(root)
	b := page.Bounds()
	if _, err := raglit.WriteRegionDoc(doc, raglit.RegionPage{
		Page: 1, WidthIn: wIn, HeightIn: hIn, DPI: 200,
		PxW: b.Dx(), PxH: b.Dy(), Root: root, Text: text, Spans: spans,
	}); err != nil {
		t.Fatal(err)
	}

	// --strict: a re-rasterization that does not reproduce the recorded pixels,
	// or a crop that does not reproduce the recorded digest, is a failure here.
	out := filepath.Join(dir, "crop.png")
	if err := runRegion([]string{"--strict", "--out", out, doc, root.Children[0].ID}); err != nil {
		t.Fatalf("re-rendering a crop out of a re-rasterized PDF did not reproduce: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := raglit.VerifyRegionRender(root.Children[0], got); err != nil {
		t.Error(err)
	}
}

// A quotation is what a consumer actually holds. It has to resolve to the region
// that produced it, or nothing above can show the right crop.
func TestRegionCommandLocatesAQuotation(t *testing.T) {
	doc, root := recordedSheet(t)
	d, ok, err := raglit.ReadRegionDoc(doc)
	if err != nil || !ok {
		t.Fatalf("read back: %v %v", err, ok)
	}
	hits := locateRegions(d, "lies westerly of the centerline")
	if len(hits) != 1 {
		t.Fatalf("want the one text block, got %d", len(hits))
	}
	if hits[0].ID != root.Children[0].ID {
		t.Errorf("quotation resolved to %s, want %s", hits[0].ID, root.Children[0].ID)
	}
	if n := len(allRegions(d)); n != 2 {
		t.Errorf("--list should show both regions, got %d", n)
	}
}
