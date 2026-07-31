package attest

import (
	"strings"
	"testing"
)

// The reviewer's terms are QUOTED. A tool restating a qualified attestation in
// its own words has changed what somebody attested to.
func TestProvenanceQuotesTheAffirmation(t *testing.T) {
	stmt := "I went through this document and am reasonably certain there are only minor " +
		"errors or mis-transcriptions that should not be materially relevant, to the best of my ability."
	r := sealed(t, turn(0, 1, "A", "one"), turn(1, 2, "B", "two"), turn(2, 3, "B", "three"))
	st, err := Resolve(r, []Entry{
		{Kind: Corrected, Unit: r.Units[0].ID, Text: "ONE", By: "K. Osei"},
		{Kind: Affirmed, Blanket: true, By: "K. Osei", At: "2026-07-20T14:02:11Z", Statement: stmt},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := st.Provenance()
	if !strings.Contains(p, stmt) {
		t.Errorf("the affirmation was not quoted:\n%s", p)
	}
	if strings.Contains(p, "the rest is right") {
		t.Error("the tool paraphrased a qualified attestation")
	}
	// A pass that went through everything must not read as a thin one.
	if strings.Contains(p, "rather than judged one by one") {
		t.Errorf("affirmed units described as unjudged:\n%s", p)
	}
	if !strings.Contains(p, "accepted under the reviewer's affirmation") {
		t.Errorf("affirmed units not described as accepted:\n%s", p)
	}
	if st.Stats.SweptStatement != stmt {
		t.Error("statement lost from stats")
	}
}

// An affirmation whose terms were never recorded — anything imported from a tool
// with no field for them — must say so rather than have terms invented for it.
func TestUnrecordedTermsAreNamedAsSuch(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"))
	st, err := Resolve(r, []Entry{{Kind: Affirmed, Blanket: true, By: "K. Osei", At: "2026-07-20T14:02:11Z"}})
	if err != nil {
		t.Fatal(err)
	}
	p := st.Provenance()
	if !strings.Contains(p, "terms were not recorded") {
		t.Errorf("a termless affirmation did not say so:\n%s", p)
	}
}

// Untouched is the only state that means nothing is known, and it must be loud.
func TestUntouchedIsDistinctFromAffirmed(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"), turn(1, 2, "B", "two"))
	st, err := Resolve(r, []Entry{{Kind: Confirmed, Unit: r.Units[0].ID, By: "K. Osei"}})
	if err != nil {
		t.Fatal(err)
	}
	p := st.Provenance()
	if !strings.Contains(p, "UNTOUCHED") || !strings.Contains(p, "nothing here says") {
		t.Errorf("an unread turn was not reported as unread:\n%s", p)
	}
}

// Withdrawing an affirmation drops its terms with it.
func TestWithdrawingAnAffirmationDropsItsTerms(t *testing.T) {
	r := sealed(t, turn(0, 1, "A", "one"))
	st, err := Resolve(r, []Entry{
		{Kind: Affirmed, Blanket: true, By: "K. Osei", Statement: "went through it"},
		{Kind: Retract, Blanket: true, By: "K. Osei"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Stats.SweptStatement != "" || st.Stats.Affirmed != 0 {
		t.Fatalf("withdrawn affirmation kept its terms: %+v", st.Stats)
	}
}
