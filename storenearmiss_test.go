package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// registerTestDocTypes registers two types with descriptions that have
// distinctive keywords, so we can verify the keyword-matching logic.
func registerTestDocTypes(t *testing.T, s *Store) {
	t.Helper()
	if err := s.SetDocType(DocType{
		Name:        "work order",
		Description: "A work order issued by a garage, listing parts and labour",
		Schema:      json.RawMessage(`{"type":"object","properties":{"parts":{"type":"string"}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDocType(DocType{
		Name:        "survey",
		Description: "A boundary survey report with lot and parcel descriptions",
		Schema:      json.RawMessage(`{"type":"object","properties":{"lot":{"type":"string"}}}`),
	}); err != nil {
		t.Fatal(err)
	}
}

// recordMachineIdentity records a machine-sourced identity (gen_source='machine'),
// which is what DocTypeNearMisses counts.
func recordMachineIdentity(t *testing.T, s *Store, ctx context.Context, path string, d DocIdentity) {
	t.Helper()
	d.Source = IdentityByMachine
	if err := s.SetDocumentIdentity(ctx, path, d); err != nil {
		t.Fatal(err)
	}
}

func TestDocTypeNearMisses_Basic(t *testing.T) {
	s := storeWithDocs(t, 5)
	ctx := context.Background()
	registerTestDocTypes(t, s)
	p := func(i int) string { return fmt.Sprintf("/corpus/scan_%03d.pdf", i) }

	// doc 0: captioned, untyped, matches "work order" (contains "work order")
	recordMachineIdentity(t, s, ctx, p(0), DocIdentity{
		Name: "Work order for the shop", Summary: "A work order listing parts and labour.",
	})

	// doc 1: captioned, untyped, matches "survey" (contains "survey")
	recordMachineIdentity(t, s, ctx, p(1), DocIdentity{
		Name: "Boundary survey", Summary: "A survey report with lot descriptions.",
	})

	// doc 2: captioned, untyped, matches both types (contains "work order" and "survey")
	recordMachineIdentity(t, s, ctx, p(2), DocIdentity{
		Name: "Work order referencing a survey", Summary: "A work order and a boundary survey.",
	})

	// doc 3: captioned, TYPED as work order → not a near-miss for any type
	recordMachineIdentity(t, s, ctx, p(3), DocIdentity{
		Name: "Work order", Summary: "A work order.", DocType: "work order",
	})

	// doc 4: untyped, NOT captioned → not a near-miss

	out, err := s.DocTypeNearMisses()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]int{}
	for _, nm := range out {
		got[nm.TypeName] = nm.Count
	}

	// work order: doc 0, doc 2 → 2
	if got["work order"] != 2 {
		t.Errorf("work order near-miss count = %d, want 2", got["work order"])
	}
	// survey: doc 1, doc 2 → 2
	if got["survey"] != 2 {
		t.Errorf("survey near-miss count = %d, want 2", got["survey"])
	}
	// Typed doc (doc 3) is excluded from all counts.
}

func TestDocTypeNearMisses_NoTypes(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	recordMachineIdentity(t, s, ctx, "/corpus/scan_000.pdf", DocIdentity{
		Name: "Some caption", Summary: "A summary.",
	})
	out, err := s.DocTypeNearMisses()
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("DocTypeNearMisses with no types = %v, want nil", out)
	}
}

func TestDocTypeNearMisses_NoUntypedDocs(t *testing.T) {
	s := storeWithDocs(t, 2)
	ctx := context.Background()
	registerTestDocTypes(t, s)

	// doc 0: captioned and typed
	recordMachineIdentity(t, s, ctx, "/corpus/scan_000.pdf", DocIdentity{
		Name: "Work order", Summary: "A work order.", DocType: "work order",
	})
	// doc 1: captioned and typed
	recordMachineIdentity(t, s, ctx, "/corpus/scan_001.pdf", DocIdentity{
		Name: "Survey", Summary: "A boundary survey.", DocType: "survey",
	})

	out, err := s.DocTypeNearMisses()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("DocTypeNearMisses with all docs typed = %v, want empty", out)
	}
}

func TestDocTypeNearMisses_NoMatchingKeywords(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	registerTestDocTypes(t, s)

	// Caption has no words in common with either description.
	recordMachineIdentity(t, s, ctx, "/corpus/scan_000.pdf", DocIdentity{
		Name: "A purchase agreement", Summary: "An agreement between two parties.",
	})

	out, err := s.DocTypeNearMisses()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("DocTypeNearMisses with no keyword match = %v, want empty", out)
	}
}

func TestDocTypeNearMisses_EmptyIndex(t *testing.T) {
	s := storeWithDocs(t, 0)
	out, err := s.DocTypeNearMisses()
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("DocTypeNearMisses on empty index = %v, want nil", out)
	}
}

// TestDocTypeNearMisses_ExcludesPersonSourced verifies that a person-sourced
// untyped doc matching a type's keywords is NOT counted as a near-miss.
// A person who captioned a doc and left it typeless made a deliberate call;
// surfacing their own decision as a coverage warning is the over-claiming
// direction F23 exists to prevent.
func TestDocTypeNearMisses_ExcludesPersonSourced(t *testing.T) {
	s := storeWithDocs(t, 3)
	ctx := context.Background()
	registerTestDocTypes(t, s)

	// doc 0: machine-sourced, untyped, matches "work order" → COUNTED
	recordMachineIdentity(t, s, ctx, "/corpus/scan_000.pdf", DocIdentity{
		Name: "Work order for the shop", Summary: "A work order listing parts and labour.",
	})

	// doc 1: PERSON-sourced (via RecordIdentity), untyped, matches "work order"
	// → NOT counted. This is the regression the tech lead's boundary decision
	// calls out.
	if _, err := s.RecordIdentity(ctx, "/corpus/scan_001.pdf", DocIdentity{
		Name: "Work order for the garage", Summary: "A work order with parts.",
	}, "alice"); err != nil {
		t.Fatal(err)
	}

	// doc 2: machine-sourced, untyped, matches "survey" → COUNTED
	recordMachineIdentity(t, s, ctx, "/corpus/scan_002.pdf", DocIdentity{
		Name: "Boundary survey", Summary: "A survey report with lot descriptions.",
	})

	out, err := s.DocTypeNearMisses()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]int{}
	for _, nm := range out {
		got[nm.TypeName] = nm.Count
	}

	// work order: only doc 0 (machine) is counted; doc 1 (person) is excluded.
	if got["work order"] != 1 {
		t.Errorf("work order near-miss count = %d, want 1 (person-sourced excluded)", got["work order"])
	}
	// survey: doc 2 → 1
	if got["survey"] != 1 {
		t.Errorf("survey near-miss count = %d, want 1", got["survey"])
	}
}

// TestDocTypeKeywords_ChecksBasicExtraction verifies the keyword extraction
// logic directly without a store.
func TestDocTypeKeywords_ChecksBasicExtraction(t *testing.T) {
	keys := docTypeKeywords("A work order issued by a garage, listing parts and labour")
	got := map[string]bool{}
	for _, k := range keys {
		got[k] = true
	}

	// Content words should be present.
	for _, want := range []string{"work", "order", "garage", "listing", "parts", "labour"} {
		if !got[want] {
			t.Errorf("keywords missing %q; got %v", want, keys)
		}
	}

	// Stop words should be absent.
	for _, sw := range []string{"a", "by", "the", "and"} {
		if got[sw] {
			t.Errorf("keywords should not contain stop word %q; got %v", sw, keys)
		}
	}
}
