package raglit

import "testing"

// The real one, from a live corpus: every page of a digitally-signed PDF carries
// the Authentisign header, which is 46 letters and digits against a threshold of
// 24. A SCANNED exhibit page with nothing else on it was accepted as a text-layer
// page and never OCR'd.
func TestSigningOverlayDoesNotCountAsPageText(t *testing.T) {
	const hdr = "Authentisign ID: 0462D64D-B418-4A0D-A59D-590A2A8C9F0D"
	pages := []string{
		hdr + "\nForm 21 Residential Purchase and Sale Agreement\nThe parties agree as follows, at length.",
		hdr, // a scanned Exhibit A: header only
		hdr + "\nEXHIBIT A legal description text that is genuinely present here.",
		hdr, // another scan
	}
	if textLayerContent(hdr) < pdfTextThreshold {
		t.Fatalf("premise wrong: the header alone is %d content chars, under the threshold",
			textLayerContent(hdr))
	}
	boiler := pageBoilerplate(pages)
	if len(boiler) == 0 {
		t.Fatal("the repeated header was not detected as boilerplate")
	}
	for i, p := range pages {
		got := textLayerContent(stripLines(p, boiler)) >= pdfTextThreshold
		want := i == 0 || i == 2
		if got != want {
			t.Errorf("page %d: hasText=%v, want %v", i+1, got, want)
		}
	}
}

// A header on only some pages is not boilerplate — dropping it would lose real
// text from the pages that carry it.
func TestBoilerplateNeedsNearlyEveryPage(t *testing.T) {
	pages := []string{"SHARED HEADER LINE HERE\nalpha", "SHARED HEADER LINE HERE\nbeta", "gamma", "delta", "epsilon"}
	if b := pageBoilerplate(pages); len(b) != 0 {
		t.Errorf("a line on 2 of 5 pages was treated as boilerplate: %v", b)
	}
}

// Too few pages to tell a header from content.
func TestBoilerplateNeedsThreePages(t *testing.T) {
	if b := pageBoilerplate([]string{"SAME LINE", "SAME LINE"}); b != nil {
		t.Errorf("two pages should not yield boilerplate, got %v", b)
	}
}

// Short lines are not distinguishing and must not be stripped — a page number or
// a one-word label is not a header.
func TestBoilerKeyIgnoresShortLines(t *testing.T) {
	if k := boilerKey("p. 3"); k != "" {
		t.Errorf("short line yielded key %q", k)
	}
	if k := boilerKey("Authentisign ID: 0462D64D"); k == "" {
		t.Error("a real header yielded no key")
	}
}

// The space-padded watermark case that motivated textLayerContent still holds.
func TestWatermarkStillUnderThreshold(t *testing.T) {
	wm := "U N O F F I C I A L      D O C U M E N T"
	if textLayerContent(wm) >= pdfTextThreshold {
		t.Errorf("watermark counted as %d content chars", textLayerContent(wm))
	}
}
