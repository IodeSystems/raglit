package raglit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// The policy: raglit READS a PDF page. A text layer is whatever the producer
// chose to put there, and in this corpus that was repeatedly not the page — a
// watermark, an e-signature envelope id, a stamp over a 300 dpi scan. Three
// successive sharpenings of "does this text layer look real" each missed the
// next case, and each miss was discovered by a person finding the index did not
// hold a document it claimed to.
//
// This is the fixture that fooled every one of them: a scanned page with a
// signing stamp for a text layer.
func TestPDFUnits_ReadsThePageRatherThanItsTextLayer(t *testing.T) {
	if !HavePoppler() {
		t.Skip("poppler not installed")
	}
	dir := t.TempDir()
	scan := renderTextPDF(t, dir, "LEAD-BASED PAINT DISCLOSURE")
	stamped := filepath.Join(dir, "stamped.pdf")
	const stamp = "Authentisign ID: 2311E4FA-9A15-4ECE-ADBB-E5C15A33BDE7"
	if err := api.AddTextWatermarksFile(scan, stamped, nil, true, stamp,
		"points:8, scale:1 abs, pos:tl, rot:0, op:1", model.NewDefaultConfiguration()); err != nil {
		t.Skipf("cannot stamp a text layer with this pdfcpu: %v", err)
	}
	// The premise: poppler sees a text layer, and it is the stamp, not the page.
	texts, err := pdftotextPages(context.Background(), stamped)
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) == 0 || !strings.Contains(texts[0], "Authentisign") {
		t.Fatalf("fixture has no signing overlay in its text layer: %q", texts)
	}

	units, err := pdfUnits(context.Background(), stamped, true, nil, RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("units = %d, want 1", len(units))
	}
	if !units[0].isImage() {
		t.Fatalf("the page was taken as text: %q — a text layer is not a reading of the page", units[0].text)
	}
}

// With nothing able to read the pages, the text layer is all there is. Taking it
// beats indexing an empty document — and it is the ONLY case where a page enters
// the index unread.
func TestPDFUnits_FallsBackToTheTextLayerWithNoOCR(t *testing.T) {
	if !HavePoppler() {
		t.Skip("poppler not installed")
	}
	dir := t.TempDir()
	pdf := renderTextPDF(t, dir, "anything")
	units, err := pdfUnits(context.Background(), pdf, false, nil, RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].isImage() {
		t.Fatalf("units = %+v, want one text unit", units)
	}
}

// A page that fails must not take the document with it. Measured 2026-08-06: a
// looping bearing table on page 2 of a survey discarded page 1, which had
// already read perfectly — 7/17 on the bench instead of ~12/17.
func TestOnePageFailureDoesNotLoseTheDocument(t *testing.T) {
	pages := []PageText{
		{Page: 1, Text: "GOOD PAGE", Engine: "vision"},
		{Page: 2, Engine: "failed", Err: "the model repeated a 119-character block"},
	}
	if allFailed(pages) {
		t.Error("one failed page among readable ones is not a failed document")
	}
	// A page that failed must be distinguishable from a blank one.
	if pages[1].Err == "" {
		t.Error("a failed page must carry its reason")
	}
	if pages[1].Text != "" {
		t.Error("a failed page must not present text it does not have")
	}
	// Every page failing IS a failure — otherwise an empty document reports success.
	if !allFailed([]PageText{{Page: 1, Engine: "failed", Err: "x"}}) {
		t.Error("a document where nothing read must fail")
	}
	// A legitimately blank page has no Err and must not count as failed.
	if allFailed([]PageText{{Page: 1, Text: "", Engine: "vision"}}) {
		t.Error("a blank page is not a failed page")
	}
}
