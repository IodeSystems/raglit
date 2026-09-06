package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit"
)

// strokedPage is ink on paper with edges to lose.
func strokedPage(w, h int, blurPasses int) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	for y := 5; y < h-5; y += 7 {
		for x := 5; x < w-5; x++ {
			g.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	// A crude box blur, repeated: enough to take the edge energy down without
	// pulling opencv into the test binary.
	for p := 0; p < blurPasses; p++ {
		src := *g
		out := image.NewGray(g.Bounds())
		for y := 1; y < h-1; y++ {
			for x := 1; x < w-1; x++ {
				sum := 0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						sum += int(src.GrayAt(x+dx, y+dy).Y)
					}
				}
				out.SetGray(x, y, color.Gray{Y: uint8(sum / 9)})
			}
		}
		g = out
	}
	return g
}

// writeRecordedRead lays down a page image and a region sidecar whose digest
// matches it — the shape of a read taken before anything measured pixels.
func writeRecordedRead(t *testing.T, dir string, page *image.Gray) string {
	t.Helper()
	docPath := filepath.Join(dir, "sheet.png")
	f, err := os.Create(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, page); err != nil {
		t.Fatal(err)
	}
	f.Close()

	root := &raglit.Region{Page: 1, BBox: raglit.Rect{X: 0, Y: 0, W: 1, H: 1}, DPI: 200,
		Text: "whatever it said at the time"}
	crop, err := raglit.RerenderRegion(page, root)
	if err != nil {
		t.Fatal(err)
	}
	root.SHA256 = raglit.RegionSHA(crop)
	root.ID = "p1"
	if _, err := raglit.WriteRegionDoc(docPath, raglit.RegionPage{
		Page: 1, WidthIn: 8.5, HeightIn: 11, DPI: 200,
		PxW: page.Bounds().Dx(), PxH: page.Bounds().Dy(), Root: root,
	}); err != nil {
		t.Fatal(err)
	}
	return docPath
}

func flagsOf(t *testing.T, docPath string) []string {
	t.Helper()
	doc, ok, err := raglit.ReadRegionDoc(docPath)
	if err != nil || !ok {
		t.Fatalf("sidecar unreadable: %v ok=%v", err, ok)
	}
	return doc.Pages[0].Root.Flags
}

// The point of the pass: it costs no model call, and it can only say something
// about an image it has REPRODUCED.
func TestBackfillFlagsADamagedRecordedRead(t *testing.T) {
	dir := t.TempDir()
	doc := writeRecordedRead(t, dir, strokedPage(300, 300, 4))

	tally, found, hits, err := backfillOne(doc, false)
	if err != nil || !found {
		t.Fatalf("backfill did not run: %v found=%v", err, found)
	}
	if tally.mismatched != 0 {
		t.Fatalf("a freshly recorded read did not reproduce: %+v", tally)
	}
	if tally.flagged != 1 || len(hits) != 1 {
		t.Fatalf("a blurred page was not flagged: %+v hits=%v", tally, hits)
	}
	// Reporting only — the sidecar must be untouched without --apply.
	if got := flagsOf(t, doc); len(got) != 0 {
		t.Errorf("a dry run wrote flags: %v", got)
	}

	if _, _, _, err := backfillOne(doc, true); err != nil {
		t.Fatal(err)
	}
	got := flagsOf(t, doc)
	found = false
	for _, f := range got {
		if f == raglit.FlagBlurred {
			found = true
		}
	}
	if !found {
		t.Errorf("--apply did not record the flag: %v", got)
	}
}

func TestBackfillLeavesACleanReadAlone(t *testing.T) {
	dir := t.TempDir()
	doc := writeRecordedRead(t, dir, strokedPage(300, 300, 0))

	tally, _, hits, err := backfillOne(doc, true)
	if err != nil {
		t.Fatal(err)
	}
	if tally.flagged != 0 || len(hits) != 0 {
		t.Errorf("a sharp page was flagged as damaged: %+v %v", tally, hits)
	}
	if tally.unchanged != 1 {
		t.Errorf("expected one clean region, got %+v", tally)
	}
	if got := flagsOf(t, doc); len(got) != 0 {
		t.Errorf("a clean read gained flags: %v", got)
	}
}

// The refusal that matters. A flag written from a different image than the one
// the text came from is a false statement about provenance, and this record
// exists so provenance means something.
func TestBackfillRefusesToMeasureAnImageThatDoesNotReproduce(t *testing.T) {
	dir := t.TempDir()
	doc := writeRecordedRead(t, dir, strokedPage(300, 300, 4))

	// The page changes underneath the record — a re-rasterization, a re-scan.
	f, err := os.Create(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, strokedPage(300, 300, 1)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	tally, _, hits, err := backfillOne(doc, true)
	if err != nil {
		t.Fatal(err)
	}
	if tally.mismatched != 1 {
		t.Fatalf("a changed page was not caught: %+v", tally)
	}
	if tally.flagged != 0 || len(hits) != 0 {
		t.Errorf("measured an image the text did not come from: %+v %v", tally, hits)
	}
	if got := flagsOf(t, doc); len(got) != 0 {
		t.Errorf("wrote flags from an unreproducible render: %v", got)
	}
}

// Running it twice must not accumulate.
func TestBackfillIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	doc := writeRecordedRead(t, dir, strokedPage(300, 300, 4))

	if _, _, _, err := backfillOne(doc, true); err != nil {
		t.Fatal(err)
	}
	first := len(flagsOf(t, doc))
	tally, _, _, err := backfillOne(doc, true)
	if err != nil {
		t.Fatal(err)
	}
	if second := len(flagsOf(t, doc)); second != first {
		t.Errorf("flags accumulated: %d then %d", first, second)
	}
	if tally.flagged != 0 {
		t.Errorf("a second pass reported new flags: %+v", tally)
	}
}
