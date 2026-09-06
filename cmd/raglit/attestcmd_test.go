package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit"
	"github.com/iodesystems/raglit/attest"
)

// sheet draws something with enough structure that two different crops of it
// cannot digest the same.
func sheet() image.Image {
	im := image.NewRGBA(image.Rect(0, 0, 800, 600))
	draw.Draw(im, im.Bounds(), &image.Uniform{color.RGBA{250, 249, 244, 255}}, image.Point{}, draw.Src)
	for i := 0; i < 20; i++ {
		draw.Draw(im, image.Rect(40, 40+i*12, 40+300-i*7, 46+i*12),
			&image.Uniform{color.RGBA{60, 60, 60, 255}}, image.Point{}, draw.Src)
	}
	draw.Draw(im, image.Rect(420, 60, 760, 540),
		&image.Uniform{color.RGBA{200, 214, 235, 255}}, image.Point{}, draw.Src)
	return im
}

// writeSheet puts a PNG on disk and returns the crop digests raglit would have
// recorded for the regions below — computed the same way raglit computes them,
// which is the point of the exercise.
func writeSheet(t *testing.T) (string, image.Image) {
	t.Helper()
	im := sheet()
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "survey.png")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p, im
}

func regionDoc(t *testing.T, im image.Image) *raglit.RegionDoc {
	t.Helper()
	mk := func(bbox raglit.Rect, kind, text string) *raglit.Region {
		r := &raglit.Region{BBox: bbox, Kind: kind, Text: text, Page: 1, DPI: 300}
		b, err := raglit.RerenderRegion(im, r)
		if err != nil {
			t.Fatal(err)
		}
		r.SHA256 = attest.SHA256Hex(b)
		return r
	}
	root := mk(raglit.Rect{X: 0, Y: 0, W: 1, H: 1}, "overview", "Plat of survey, lot C.")
	root.Flags = []string{raglit.FlagLowResolution}
	root.TokensPerSqIn = 4
	desc := mk(raglit.Rect{X: 0.04, Y: 0.05, W: 0.42, H: 0.45}, "text-block", "REPLAT OF BLOCK 4.")
	desc.Depth = 1
	inner := mk(raglit.Rect{X: 0.5, Y: 0.09, W: 0.44, H: 0.82}, "drawing", "Lot grid.")
	inner.Depth = 1
	inner.Flags = []string{raglit.FlagExhausted}
	root.Children = []*raglit.Region{desc, inner}

	// Ids are assigned the way a real read assigns them: parent before children.
	root.ID = "p1"
	desc.ID = "p1.0"
	inner.ID = "p1.1"

	b := im.Bounds()
	text, spans := raglit.RegionTranscript(root)
	return &raglit.RegionDoc{
		Doc:     "survey.png",
		Version: 1,
		Pages: []raglit.RegionPage{{
			Page: 1, DPI: 300, PxW: b.Dx(), PxH: b.Dy(),
			Root: root, Text: text, Spans: spans,
		}},
	}
}

// The descent is the point of raglit's region read, and flattening it would put
// an overview and the leaves refining it side by side as peers that happen to
// repeat one another.
func TestReadingKeepsTheDescentAsAParentChain(t *testing.T) {
	path, im := writeSheet(t)
	rd, err := readingFromRegions(path, regionDoc(t, im))
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.Seal(); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(rd.Units) != 3 {
		t.Fatalf("units = %d, want 3", len(rd.Units))
	}
	root := rd.Units[0]
	if root.Parent != "" {
		t.Errorf("the page root has a parent: %q", root.Parent)
	}
	kids := rd.Children(root.ID)
	if len(kids) != 2 {
		t.Fatalf("root has %d children, want 2", len(kids))
	}
	if rd.Asset.Kind != attest.KindImage || rd.Producer != "raglit/regions" {
		t.Errorf("asset/producer wrong: %+v %q", rd.Asset, rd.Producer)
	}
}

// raglit already digests the exact crop as its cycle detector. Carrying it
// through unchanged is the whole reason the review page can say "this IS the
// image the words came from".
func TestEvidenceRoundTripsToTheRecordedDigest(t *testing.T) {
	path, im := writeSheet(t)
	doc := regionDoc(t, im)
	if _, err := raglit.WriteRegionDoc(path, doc.Pages[0]); err != nil {
		t.Fatal(err)
	}
	rd, err := readingFromRegions(path, doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.Seal(); err != nil {
		t.Fatal(err)
	}
	if ok, id := rd.Reproducible(); !ok {
		t.Fatalf("unit %s carries no evidence", id)
	}

	ev := regionEvidence{root: filepath.Dir(path)}
	for _, u := range rd.Units {
		art, err := ev.Render(context.Background(), rd.Asset, u)
		if err != nil {
			t.Fatalf("render %s: %v", u.ID, err)
		}
		if err := attest.VerifyEvidence(u, art); err != nil {
			t.Errorf("%v", err)
		}
		if art.MIME != "image/png" {
			t.Errorf("mime = %q", art.MIME)
		}
	}
}

// The join back to a raglit region is by recorded geometry, because attest
// content-addresses its units and the raglit path id does not survive into
// them. A sheet re-read since the reading was written must be reported, never
// matched to the nearest thing.
func TestARereadIsReportedNotGuessedAt(t *testing.T) {
	path, im := writeSheet(t)
	doc := regionDoc(t, im)
	if _, err := raglit.WriteRegionDoc(path, doc.Pages[0]); err != nil {
		t.Fatal(err)
	}
	rd, _ := readingFromRegions(path, doc)
	_ = rd.Seal()

	// A second read proposes different boxes. The old reading's units now
	// describe geometry the sidecar no longer records.
	moved := regionDoc(t, im)
	moved.Pages[0].Root.Children[0].BBox = raglit.Rect{X: 0.06, Y: 0.07, W: 0.40, H: 0.40}
	if _, err := raglit.WriteRegionDoc(path, moved.Pages[0]); err != nil {
		t.Fatal(err)
	}

	ev := regionEvidence{root: filepath.Dir(path)}
	_, err := ev.Render(context.Background(), rd.Asset, rd.Units[1])
	if err == nil {
		t.Fatal("a unit whose region no longer exists rendered anyway")
	}
	if !contains(err.Error(), "re-read") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

func TestParentOf(t *testing.T) {
	for in, want := range map[string]string{
		"p1": "", "p1.0": "p1", "p1.0.2": "p1.0", "p12.3.4": "p12.3",
	} {
		if got := parentOf(in); got != want {
			t.Errorf("parentOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// A page rasterized to a different size at the same nominal dpi means a
// different renderer. Saying THAT is far more useful than an unexplained digest
// mismatch two steps downstream.
func TestRasterizationDriftIsNamed(t *testing.T) {
	path, im := writeSheet(t)
	doc := regionDoc(t, im)
	doc.Pages[0].PxW = 999 // as if pdftoppm had changed under us
	if _, err := raglit.WriteRegionDoc(path, doc.Pages[0]); err != nil {
		t.Fatal(err)
	}
	rd, _ := readingFromRegions(path, doc)
	_ = rd.Seal()
	_, err := regionEvidence{root: filepath.Dir(path)}.Render(context.Background(), rd.Asset, rd.Units[0])
	if err == nil {
		t.Fatal("a page that rasterized to the wrong size was cropped anyway")
	}
	if !contains(err.Error(), "does not reproduce") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && bytes.Contains([]byte(s), []byte(sub))
}
