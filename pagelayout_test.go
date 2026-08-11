package raglit

import (
	"strings"
	"testing"
)

const sampleMarkup = `<div data-bbox="59 47 550 63" data-label="Section-Header"><b>TRANSACTION MEMO</b> (COMPLETED BY SELLING AGENT)</div><div data-bbox="59 90 300 106" data-label="Text"><p>EARNEST MONEY HELD BY: Escrow</p></div><div data-bbox="59 120 250 136" data-label="Text"><p>DATE: 5/27/2021</p></div>`

// Each box must carry ITS OWN text. Getting the pairing wrong is the failure
// that would make the whole view lie confidently: a box drawn on one line
// showing another line's words is worse than no boxes at all.
func TestBoxesCarryTheirOwnText(t *testing.T) {
	b := ParseLayoutBoxes(sampleMarkup)
	if len(b) != 3 {
		t.Fatalf("want 3 boxes, got %d: %+v", len(b), b)
	}
	if !strings.Contains(b[0].Text, "TRANSACTION MEMO") || strings.Contains(b[0].Text, "EARNEST") {
		t.Errorf("box 0 took the wrong text: %q", b[0].Text)
	}
	if !strings.Contains(b[1].Text, "EARNEST MONEY HELD BY") || strings.Contains(b[1].Text, "5/27/2021") {
		t.Errorf("box 1 took the wrong text: %q", b[1].Text)
	}
	if !strings.Contains(b[2].Text, "5/27/2021") {
		t.Errorf("box 2 lost its text: %q", b[2].Text)
	}
	if b[0].Label != "Section-Header" || b[1].Label != "Text" {
		t.Errorf("labels wrong: %q %q", b[0].Label, b[1].Label)
	}
	// Inline markup inside a block must not survive into the box text.
	if strings.Contains(b[0].Text, "<b>") {
		t.Errorf("inline tags leaked: %q", b[0].Text)
	}
}

// Coordinates are normalised 0-1000 per axis — verified by overlay on a
// 2550x3300 scan. The percentage conversion is what a renderer places boxes
// with, so it is worth pinning.
func TestPercentagesArePlaceable(t *testing.T) {
	b := LayoutBox{X0: 100, Y0: 250, X1: 600, Y1: 300}
	l, top, w, h := b.Pct()
	if l != 10 || top != 25 || w != 50 || h != 5 {
		t.Errorf("want 10/25/50/5, got %v/%v/%v/%v", l, top, w, h)
	}
}

// "No layout" is an ordinary answer. A tesseract page, a text-layer page, or a
// model that emits none must come back nil rather than erroring — the text panes
// still work and the caller renders those alone.
func TestPagesWithoutBoxesReturnNil(t *testing.T) {
	for _, in := range []string{
		"IN THE SUPERIOR COURT OF THE STATE OF WASHINGTON",
		"",
		"<p>markup but no boxes</p>",
	} {
		if b := ParseLayoutBoxes(in); b != nil {
			t.Errorf("want nil for %q, got %+v", in, b)
		}
	}
}

// Malformed coordinates must be skipped, not crash and not silently become a
// box at the origin covering nothing — a bad box in the middle must not cost
// the good ones around it.
func TestMalformedBoxesAreSkippedNotFatal(t *testing.T) {
	in := `<div data-bbox="1 2 3" data-label="Short"><p>three numbers</p></div>` +
		`<div data-bbox="10 20 30 40" data-label="Good"><p>fine</p></div>`
	b := ParseLayoutBoxes(in)
	if len(b) != 1 {
		t.Fatalf("want only the well-formed box, got %d: %+v", len(b), b)
	}
	if b[0].Label != "Good" {
		t.Errorf("kept the wrong one: %+v", b[0])
	}
}

// A block with no data-label still has a box and text. chandra omits the label
// on some blocks, and dropping those would leave holes in the overlay.
func TestUnlabelledBlocksSurvive(t *testing.T) {
	b := ParseLayoutBoxes(`<div data-bbox="5 5 100 20"><p>no label here</p></div>`)
	if len(b) != 1 {
		t.Fatalf("an unlabelled block was dropped: %+v", b)
	}
	if !strings.Contains(b[0].Text, "no label here") {
		t.Errorf("text lost: %q", b[0].Text)
	}
}
