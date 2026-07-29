package raglit

import (
	"strings"
	"testing"
)

// The read that made this necessary: a six-page summary-judgment order in a
// live matter, transcribed to nothing but the diagonal "UNOFFICIAL DOCUMENT"
// watermark read letter by letter down the page. No loop, no error, job done.
func TestAWatermarkReadDownThePageIsFlagged(t *testing.T) {
	const watermark = "T\n                    EN\n                   M\n                CU\n              DO\n         AL\n       CI\n     FI\n  OF\nUN"
	why := Suspicion(watermark)
	if why == "" {
		t.Fatal("a page of one- and two-character lines was not flagged")
	}
	if !strings.Contains(why, "DOWN the page") {
		t.Errorf("the reason does not tell the reader what it means: %q", why)
	}
}

// A genuinely short page is ordinary — a divider, a signature page, the back of
// a form. Flagging is cheap; refusing to index one would stop ingest on
// documents that are fine, so this must warn and never fail.
func TestARealPageOfTextIsNotFlagged(t *testing.T) {
	const real = `IN THE SUPERIOR COURT OF THE STATE OF WASHINGTON
IN AND FOR THE COUNTY OF HAVERN

ORDER GRANTING PARTIAL SUMMARY JUDGMENT

THIS MATTER came on for hearing before the undersigned judge, and the court
having considered the pleadings and the records and files herein, and being
fully advised in the premises, now therefore it is hereby ORDERED that the
Defendants shall cooperate as reasonably necessary to determine the location
of the sewer line along or near the shared boundary of the parties.`
	if why := Suspicion(real); why != "" {
		t.Errorf("a real page of prose was flagged: %s", why)
	}
}

func TestAnEmptyPageIsNotFlagged(t *testing.T) {
	if why := Suspicion("   \n\n  "); why != "" {
		t.Errorf("an empty page was flagged as suspect: %s", why)
	}
}

// A survey sheet is mostly drawing, and the figure description IS the read.
// Flagging it on length would mark every plat and every map as broken.
func TestAPageCarryingAFigureIsNotFlagged(t *testing.T) {
	pages := []TranscribedPage{{
		Page: 1,
		Text: "SHEET 1\nOF 2",
		Figures: []TranscribedFigure{{
			Kind:        "drawing",
			Description: "A record of survey showing Lots E through I with the railroad right-of-way.",
		}},
	}}
	if sus := SuspectPages(pages); len(sus) != 0 {
		t.Errorf("a page whose content is a described figure was flagged: %v", sus)
	}
}

// A near-empty page with no figure is worth a glance before anything is quoted.
func TestAnAlmostEmptyPageIsFlagged(t *testing.T) {
	if why := Suspicion("Exhibit A"); why == "" {
		t.Error("a page with a dozen characters was not flagged")
	}
}
