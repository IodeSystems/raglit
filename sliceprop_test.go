package raglit

import "testing"

func pg(n int, text string) PageText { return PageText{Page: n, Text: text} }

// A packet of standard forms carries its own structure: the counter resetting to
// "Page 1 of N" is the boundary, and no model has to infer it.
func TestFormBoundariesComeFromThePageCounters(t *testing.T) {
	props := ProposeSlices([]PageText{
		pg(1, "Form 21 Residential Purchase and Sale Agreement  Page 1 of 3"),
		pg(2, "Form 21  Page 2 of 3"),
		pg(3, "Form 21  Page 3 of 3"),
		pg(4, "Form 22A Financing Addendum  Page 1 of 2"),
		pg(5, "Form 22A  Page 2 of 2"),
	})
	if len(props) != 2 {
		t.Fatalf("want two instruments, got %d: %+v", len(props), props)
	}
	if props[0].From != 1 || props[0].To != 3 || props[0].Form != "21" {
		t.Errorf("first: %+v", props[0])
	}
	if props[1].From != 4 || props[1].To != 5 || props[1].Form != "22A" {
		t.Errorf("second: %+v", props[1])
	}
	for _, p := range props {
		if !p.Sliceable() {
			t.Errorf("%s should be sliceable: %+v", p.Title, p)
		}
	}
}

// Two one-page forms on one sheet cannot both be a page range, and picking one
// would mint a document from a range that is a lie about half of it. Those are
// seen-in claims, and the proposal has to say so rather than slice anyway.
func TestTwoFormsOnOneSheetAreNotProposedAsSlices(t *testing.T) {
	props := ProposeSlices([]PageText{
		pg(1, "Form 22T Addendum Page 1 of 1   Form 34 General Addendum Page 1 of 1"),
	})
	if len(props) != 2 {
		t.Fatalf("want both forms named, got %d: %+v", len(props), props)
	}
	for _, p := range props {
		if p.Sliceable() {
			t.Errorf("%s shares a sheet and must not be sliceable: %+v", p.Title, p)
		}
		if len(p.Shared) == 0 {
			t.Errorf("%s must name what it shares the sheet with: %+v", p.Title, p)
		}
	}
}

// The declared length is the form's claim; the next form's start is a fact about
// the packet. Where they disagree, the packet wins — otherwise a form reprinted
// inside another swallows it.
func TestTheNextFormsStartTruncatesAnOverlongClaim(t *testing.T) {
	props := ProposeSlices([]PageText{
		pg(1, "Form 21  Page 1 of 6"),
		pg(2, "Form 35 Inspection  Page 1 of 2"),
		pg(3, "Form 35  Page 2 of 2"),
	})
	if len(props) != 2 {
		t.Fatalf("got %d: %+v", len(props), props)
	}
	if props[0].To != 1 {
		t.Errorf("Form 21 claims 6 pages but Form 35 starts on page 2; want To=1, got %+v", props[0])
	}
}

// A bundle with no counters proposes nothing rather than guessing.
func TestNoCountersProposesNothing(t *testing.T) {
	if props := ProposeSlices([]PageText{pg(1, "A letter with no form markers at all.")}); len(props) != 0 {
		t.Errorf("want nothing proposed, got %+v", props)
	}
}

// A counter with no form number is still a run — a packet's cover pages carry
// counters and no form — so it is proposed unnamed rather than dropped.
func TestACounterWithNoFormIsStillProposed(t *testing.T) {
	props := ProposeSlices([]PageText{pg(1, "Page 1 of 2"), pg(2, "Page 2 of 2")})
	if len(props) != 1 || props[0].From != 1 || props[0].To != 2 {
		t.Fatalf("got %+v", props)
	}
	if props[0].Form != "" {
		t.Errorf("no form number was present: %+v", props[0])
	}
}
