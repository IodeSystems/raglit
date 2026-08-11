package raglit

import (
	"strings"
	"testing"
)

// The rule that is easy to get backwards and expensive when you do. An INLINE
// tag must vanish without a trace; replacing it with a space inserts whitespace
// the page never had and silently breaks exact-phrase search on text that was
// read perfectly.
//
// Note what is NOT asserted: that the superscript's CONTENT disappears. A
// footnote marker is text on the page, and a stripper that deletes page content
// to make phrases match is trading a visible problem for an invisible one. The
// marker stays; only the tag goes.
func TestInlineTagsLeaveNoSpaceBehind(t *testing.T) {
	got := StripLayoutMarkup(`<p>Afro-Shirazi<sup>1</sup>, also known as SHIRAZI</p>`)
	if strings.Contains(got, "Shirazi ,") || strings.Contains(got, "Shirazi 1") {
		t.Errorf("a space was inserted where the page had none: %q", got)
	}
	if !strings.Contains(got, "also known as SHIRAZI") {
		t.Errorf("text was lost: %q", got)
	}
	if strings.Contains(got, "<sup>") {
		t.Errorf("the tag survived: %q", got)
	}
}

// A BLOCK tag is a place the page broke, so it must become a break — otherwise
// the last word of one line fuses to the first of the next.
func TestBlockTagsBecomeLineBreaks(t *testing.T) {
	got := StripLayoutMarkup(`<div data-label="Text"><p>VOL 215</p></div><div><p>PAGE 159</p></div>`)
	if strings.Contains(got, "215PAGE") {
		t.Errorf("a block boundary fused two lines: %q", got)
	}
	if !strings.Contains(got, "VOL 215") || !strings.Contains(got, "PAGE 159") {
		t.Errorf("text was lost: %q", got)
	}
	// <br/> is the same statement in shorter form.
	if g := StripLayoutMarkup(`a<br/>b`); strings.Contains(g, "ab") {
		t.Errorf("<br/> did not break the line: %q", g)
	}
}

// The alt text of a figure is the ONLY text a photograph or a barcode ever
// produces — the pipeline asks for it deliberately. Dropping the tag must not
// drop the description.
func TestFigureDescriptionsSurvive(t *testing.T) {
	in := `<div data-label="Image"><img alt="Barcode with number 200809090112 and Skagit County Auditor stamp dated 9/8/2008."/></div>`
	got := StripLayoutMarkup(in)
	if !strings.Contains(got, "200809090112") || !strings.Contains(got, "Skagit County Auditor") {
		t.Errorf("the figure description was thrown away: %q", got)
	}
	if strings.Contains(got, "<img") || strings.Contains(got, "alt=") {
		t.Errorf("markup survived: %q", got)
	}
}

// Coordinates must not reach an embedding. `data-bbox="596 138 728 156"` encodes
// as four numbers that mean nothing to a searcher and everything to a vector.
func TestBoundingBoxesDoNotReachTheIndex(t *testing.T) {
	got := StripLayoutMarkup(`<div data-bbox="596 138 728 156" data-label="Page-Header"><p>VOL 215 PAGE 159</p></div>`)
	for _, junk := range []string{"596", "data-bbox", "Page-Header", "138"} {
		if strings.Contains(got, junk) {
			t.Errorf("%q survived into the indexed text: %q", junk, got)
		}
	}
	if !strings.Contains(got, "VOL 215 PAGE 159") {
		t.Errorf("the actual content did not survive: %q", got)
	}
}

// Entities are characters to a searcher. Nobody types &amp;.
func TestEntitiesBecomeCharacters(t *testing.T) {
	got := StripLayoutMarkup(`<p>Smith &amp; Jones &quot;the parties&quot;</p>`)
	if !strings.Contains(got, `Smith & Jones "the parties"`) {
		t.Errorf("entities not decoded: %q", got)
	}
}

// A table row stays ONE line, so a phrase spanning two cells still matches.
func TestTableCellsStayOnOneLine(t *testing.T) {
	got := StripLayoutMarkup(`<tr><td>Lawrence McKinnon</td><td>05/25/2021</td></tr>`)
	first := strings.SplitN(strings.TrimSpace(got), "\n", 2)[0]
	if !strings.Contains(first, "Lawrence McKinnon") || !strings.Contains(first, "05/25/2021") {
		t.Errorf("a row was split across lines: %q", got)
	}
}

// Plain text must come through untouched — the fast path, and the guarantee that
// wiring this in cannot damage a corpus that never had markup.
func TestPlainTextIsUnchanged(t *testing.T) {
	for _, in := range []string{
		"IN THE SUPERIOR COURT OF THE STATE OF WASHINGTON",
		"1. CONVEYANCE\n\nX IS a Lot of Record as defined in SCC 14.04.020.",
		"",
	} {
		if got := StripLayoutMarkup(in); got != in {
			t.Errorf("plain text changed:\n in: %q\nout: %q", in, got)
		}
	}
}

// A math or legal document can contain a bare < that is not a tag. Dropping to
// end-of-string on it would eat the rest of the page.
func TestBareAngleBracketDoesNotEatTheDocument(t *testing.T) {
	got := StripLayoutMarkup("the parcel is < 2 acres and the setback is > 30 feet")
	if !strings.Contains(got, "2 acres") || !strings.Contains(got, "30 feet") {
		t.Errorf("content after a bare angle bracket was lost: %q", got)
	}
}

// The composed entry point does both jobs: markup out, table pipes flattened.
func TestFlattenForIndexDoesBoth(t *testing.T) {
	in := "<div><p>Header</p></div>\n| fact | truth |\n|---|---|\n| cap | MOWRER |"
	got := FlattenForIndex(in)
	if strings.Contains(got, "<div") || strings.Contains(got, "|") {
		t.Errorf("markup or pipes survived: %q", got)
	}
	if !strings.Contains(got, "MOWRER") || !strings.Contains(got, "Header") {
		t.Errorf("content lost: %q", got)
	}
}

// The measured shape of the win, on the real fragment that started this.
func TestMarkupIsTheMajorityOfADeedsBytes(t *testing.T) {
	in := `<div data-bbox="596 138 728 156" data-label="Page-Header"><p>VOL 215 PAGE 159</p></div>
<div data-bbox="320 162 541 178" data-label="Section-Header"><p><u>W A R R A N T Y   D E E D</u></p></div>
<div data-bbox="388 178 462 194" data-label="Text"><p><b>399591</b></p></div>`
	out := FlattenForIndex(in)
	if len(out) >= len(in)/2 {
		t.Errorf("expected markup to be most of the bytes; %d -> %d", len(in), len(out))
	}
	for _, want := range []string{"VOL 215 PAGE 159", "399591"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q did not survive: %q", want, out)
		}
	}
}
