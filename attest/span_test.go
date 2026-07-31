package attest

import "testing"

// A span unit gets an id, and it is distinct from an otherwise identical unit
// located elsewhere. The whole point of the tagged union is that adding a media
// type changes nothing about the verdict algebra, so the id has to behave the
// same way for Span as it does for Area and Time.
func TestUnitIDDistinguishesSpans(t *testing.T) {
	a := Unit{Locator: Locator{Span: &Span{From: 0, To: 100}}, Text: "same words"}
	b := Unit{Locator: Locator{Span: &Span{From: 100, To: 200}}, Text: "same words"}
	if UnitID(a) == UnitID(b) {
		t.Fatal("two ranges of the same text hashed the same")
	}
	if got := UnitID(a); got[:2] != "b0" {
		t.Fatalf("span prefix = %q, want it to start b0", got)
	}
}

// A locator carrying only a span is not empty. Before Span existed, empty()
// checked two fields, and a reading built entirely of text units would have
// been rejected wholesale as unlocated.
func TestSpanLocatorIsNotEmpty(t *testing.T) {
	if (Locator{Span: &Span{From: 3, To: 9}}).empty() {
		t.Fatal("a span locator reported empty")
	}
	if !(Locator{}).empty() {
		t.Fatal("an unset locator reported non-empty")
	}
}

// Text and Span are digested separately: the same range with different words is
// a different claim, which is what makes a re-transcription land as new units
// rather than silently inheriting verdicts.
func TestSpanIDCoversText(t *testing.T) {
	a := Unit{Locator: Locator{Span: &Span{From: 0, To: 10}}, Text: "one"}
	b := Unit{Locator: Locator{Span: &Span{From: 0, To: 10}}, Text: "two"}
	if UnitID(a) == UnitID(b) {
		t.Fatal("different text at the same offsets hashed the same")
	}
}
