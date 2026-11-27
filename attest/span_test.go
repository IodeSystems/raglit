package attest

import (
	"encoding/json"
	"testing"
)

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

// TestBindingLocatorRoundTrip proves that a Binding{sentence, claim} locator
// survives a JSON round-trip, is non-empty, carries the "c-" prefix in its
// UnitID, and that two bindings differing only in sentence hash to different
// ids — all alongside the existing Time/Area/Span units in the same reading.
func TestBindingLocatorRoundTrip(t *testing.T) {
	rd := Reading{
		Asset: Asset{ID: "test-asset", Kind: KindText},
		Units: []Unit{
			{Locator: Locator{Time: &TimeSpan{Start: 0, End: 5}}, Text: "audio unit"},
			{Locator: Locator{Area: &Area{Page: 1, BBox: Rect{X: 0, Y: 0, W: 1, H: 1}}}, Text: "area unit"},
			{Locator: Locator{Span: &Span{From: 0, To: 100}}, Text: "span unit"},
			{Locator: Locator{Binding: &Binding{Sentence: "the court found that the defendant acted negligently", Claim: "defendant was negligent"}}, Text: "binding unit"},
		},
	}
	if err := rd.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Marshal to JSON.
	b, err := json.Marshal(&rd)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Unmarshal back.
	var rd2 Reading
	if err := json.Unmarshal(b, &rd2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Find the binding unit.
	var bu *Unit
	for i := range rd2.Units {
		if rd2.Units[i].Locator.Binding != nil {
			bu = &rd2.Units[i]
			break
		}
	}
	if bu == nil {
		t.Fatal("binding unit not found after round-trip")
	}

	// Sentence and claim survive.
	if bu.Locator.Binding.Sentence != "the court found that the defendant acted negligently" {
		t.Fatalf("sentence = %q", bu.Locator.Binding.Sentence)
	}
	if bu.Locator.Binding.Claim != "defendant was negligent" {
		t.Fatalf("claim = %q", bu.Locator.Binding.Claim)
	}

	// The locator is non-empty.
	if bu.Locator.empty() {
		t.Fatal("binding locator reported empty after round-trip")
	}

	// UnitID carries the binding prefix "c-".
	if len(bu.ID) < 2 || bu.ID[:2] != "c-" {
		t.Fatalf("binding unit id = %q, want prefix c-", bu.ID)
	}

	// Two bindings differing only in sentence hash to different ids.
	a := Unit{Locator: Locator{Binding: &Binding{Sentence: "sentence A", Claim: "same claim"}}, Text: "x"}
	c := Unit{Locator: Locator{Binding: &Binding{Sentence: "sentence B", Claim: "same claim"}}, Text: "x"}
	if UnitID(a) == UnitID(c) {
		t.Fatal("two bindings differing only in sentence hashed the same")
	}

	// The existing Time/Area/Span units are still present and distinct.
	if len(rd2.Units) != 4 {
		t.Fatalf("want 4 units after round-trip, got %d", len(rd2.Units))
	}
	ids := make(map[string]bool)
	for _, u := range rd2.Units {
		if u.ID == "" {
			t.Fatal("unit with empty id after round-trip")
		}
		if ids[u.ID] {
			t.Fatalf("duplicate unit id %q", u.ID)
		}
		ids[u.ID] = true
	}
}
