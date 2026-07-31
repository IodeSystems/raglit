package raglit

import (
	"context"
	"strings"
	"testing"
)

// End-to-end over a real (in-memory) index. No embedder, no OCR, no network — the
// property the whole mechanism was chosen for, so it is asserted by the tests being
// able to run at all.

func newIndex(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/index.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ingest(t *testing.T, s *Store, path string, pageTexts ...string) {
	t.Helper()
	frags := make([]Fragment, len(pageTexts))
	for i, txt := range pageTexts {
		frags[i] = Fragment{Page: i + 1, Ord: 0, Text: txt}
	}
	if err := s.Ingest(context.Background(), Document{Path: path, Title: path, Fragments: frags}); err != nil {
		t.Fatalf("ingest %s: %v", path, err)
	}
}

// The tables are declared in schema.sql, which store.go applies on every Open, so
// no migration step exists and none is needed. Pinned because "does an existing
// index need migrating?" is the first question anyone will ask.
func TestSketchTablesExistOnAFreshIndex(t *testing.T) {
	s := newIndex(t)
	if _, err := s.listShinglePages(); err != nil {
		t.Fatalf("shingle_pages missing on a fresh index: %v", err)
	}
	if _, err := s.listShingleIndex(); err != nil {
		t.Fatalf("shingle_index missing on a fresh index: %v", err)
	}
}

// An unsketched document is UNCHECKED, not clean, and the two must never be
// conflated: reporting "nothing similar" for a document nothing compared tells an
// upload flow to accept a duplicate.
func TestUnsketchedDocumentsAreReportedNotAssumedClean(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "a.pdf", deedText)
	ingest(t, s, "b.pdf", filler("other", 10))

	st, err := s.SketchStatusFor(FoldWidth, SampleMod)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sketched != 0 || len(st.Unsketched) != 2 {
		t.Fatalf("before building: sketched %d, unsketched %v", st.Sketched, st.Unsketched)
	}
	if n, errs := s.SketchAll(FoldWidth, SampleMod); n != 2 || len(errs) > 0 {
		t.Fatalf("SketchAll sketched %d, errs %v", n, errs)
	}
	st, err = s.SketchStatusFor(FoldWidth, SampleMod)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sketched != 2 || len(st.Unsketched) != 0 {
		t.Fatalf("after building: sketched %d, unsketched %v", st.Sketched, st.Unsketched)
	}
	if st.IndexRows == 0 {
		t.Error("no postings were stored")
	}
}

// A parameter change must mark exactly what it affects rather than invalidating the
// index silently — the same reason documents.frag_recipe exists.
func TestRecipeChangeMarksEverythingStale(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "a.pdf", deedText)
	if _, errs := s.SketchAll(FoldWidth, SampleMod); len(errs) > 0 {
		t.Fatal(errs)
	}
	st, err := s.SketchStatusFor(48, SampleMod)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Unsketched) != 1 {
		t.Errorf("a width change left %d documents unsketched, want 1", len(st.Unsketched))
	}
	if len(st.StaleRecipe) != 1 {
		t.Errorf("a width change reported %d stale documents, want 1", len(st.StaleRecipe))
	}
}

// The headline flow: a deed we already hold standalone turns up inside a filing.
func TestSimilarFindsAnInstrumentInsideAFiling(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "deed.pdf", deedText)
	ingest(t, s, "commitment.pdf",
		filler("schedule", 30), filler("exception", 30), deedText, filler("endorsement", 30))
	ingest(t, s, "unrelated.pdf", filler("insurance", 40))
	if _, errs := s.SketchAll(FoldWidth, SampleMod); len(errs) > 0 {
		t.Fatal(errs)
	}

	rep, err := s.SimilarIndexed("deed.pdf", SimilarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(rep.Matches), rep.Matches)
	}
	m := rep.Matches[0]
	if m.Path != "commitment.pdf" {
		t.Errorf("matched %q", m.Path)
	}
	if m.Relation != RelProbeInside {
		t.Errorf("relation = %q, want %q", m.Relation, RelProbeInside)
	}
	if len(m.Blocks) == 0 || m.Blocks[0].MatchFromPage != 3 {
		t.Errorf("alignment did not locate the deed on page 3: %+v", m.Blocks)
	}
	if !rep.Indexed {
		t.Error("a probe read from the index should say so")
	}
}

// A probe holding less text than one shingle is UNCHECKABLE, and saying so is the
// point: a blank scan reported as "nothing similar found" is a document nothing
// compared, presented as a clean result.
func TestUnshingleableProbeIsUncheckableNotClean(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "blank.pdf", "  ")
	ingest(t, s, "deed.pdf", deedText)
	if _, errs := s.SketchAll(FoldWidth, SampleMod); len(errs) > 0 {
		t.Fatal(errs)
	}
	rep, err := s.SimilarIndexed("blank.pdf", SimilarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Shingled != 0 {
		t.Fatalf("a blank page yielded %d shingles", rep.Shingled)
	}
	if len(rep.Matches) != 0 {
		t.Errorf("a blank page reported matches: %+v", rep.Matches)
	}
}

// Byte-identical files must be reported as identical, not merely similar — the
// shingle scores understate them, because masking removes the same passages from
// both sides.
func TestByteIdenticalFilesAreReportedIdentical(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "instrument.pdf", deedText)
	ingest(t, s, "misnamed-form-35.pdf", deedText)
	if err := s.SetDocumentHash("instrument.pdf", "same-bytes"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDocumentHash("misnamed-form-35.pdf", "same-bytes"); err != nil {
		t.Fatal(err)
	}
	if _, errs := s.SketchAll(FoldWidth, SampleMod); len(errs) > 0 {
		t.Fatal(errs)
	}
	rep, err := s.SimilarIndexed("instrument.pdf", SimilarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Matches) == 0 || rep.Matches[0].Relation != RelIdentical {
		t.Fatalf("want an %q match first, got %+v", RelIdentical, rep.Matches)
	}
	if rep.Matches[0].Path != "misnamed-form-35.pdf" {
		t.Errorf("identical match is %q", rep.Matches[0].Path)
	}
}

// An empty content_hash means NOT RECORDED. Grouping on it would report every
// synthetic document as a duplicate of every other.
func TestEmptyContentHashIsNotADuplicate(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "a.pdf", deedText)
	ingest(t, s, "b.pdf", filler("other", 10))
	same, err := s.SameBytesAs("a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(same) != 0 {
		t.Errorf("documents with no recorded hash were paired: %v", same)
	}
	if got, err := s.SameBytesHash(""); err != nil || len(got) != 0 {
		t.Errorf("an empty hash matched %v (err %v)", got, err)
	}
}

// The exact-bytes answer must be available WITHOUT reading the document, because
// that is the only answer available for a scan this index cannot OCR.
func TestSameBytesHashNeedsNoText(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "held.pdf", deedText)
	if err := s.SetDocumentHash("held.pdf", HashHex([]byte("the exact source bytes"))); err != nil {
		t.Fatal(err)
	}
	got, err := s.SameBytesHash(HashHex([]byte("the exact source bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["held.pdf"]; !ok {
		t.Errorf("hashing the file did not find the held copy: %v", got)
	}
}

// A recipe rebuild must CLEAR, not merely replace per document: a document deleted
// since the last build keeps its postings otherwise, and goes on generating
// candidates that resolve to a path TruePages cannot read.
func TestClearSketchesRemovesOrphanedPostings(t *testing.T) {
	s := newIndex(t)
	ingest(t, s, "a.pdf", deedText)
	if _, errs := s.SketchAll(FoldWidth, SampleMod); len(errs) > 0 {
		t.Fatal(errs)
	}
	if err := s.ClearSketches(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.listShingleIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("%d postings survived ClearSketches", len(rows))
	}
	pages, err := s.listShinglePages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 0 {
		t.Errorf("%d page rows survived ClearSketches", len(pages))
	}
}

// TruePages must honour page_spans. `fragments.page` is only a fragment's START
// page, so a stitched fragment reports every byte on the page it opened on — which
// makes "pages 12-14 of X are pages 1-3 of Y" a lie. This is page_spans' first
// production consumer; before it, the column was written by ingest, carried through
// the pool, and read by nothing but its own tests.
func TestTruePagesHonoursPageSpans(t *testing.T) {
	// One fragment covering three pages: page 1 opens it, pages 2 and 3 were
	// absorbed as continuations, so `page` is 1 for all of it.
	text := "PAGE ONE CONTENT\nPAGE TWO CONTENT\nPAGE THREE CONTENT"
	off2 := strings.Index(text, "PAGE TWO")
	off3 := strings.Index(text, "PAGE THREE")
	rows := []fragRow{{
		Page: 1, Ord: 0, Text: text,
		PageSpans: `[{"off":0,"page":1},{"off":` + itoa(off2) + `,"page":2},{"off":` + itoa(off3) + `,"page":3}]`,
	}}
	got := truePages(rows)
	if len(got) != 3 {
		t.Fatalf("got %d pages, want 3: %+v", len(got), got)
	}
	for i, want := range []string{"PAGE ONE", "PAGE TWO", "PAGE THREE"} {
		if got[i].Page != i+1 {
			t.Errorf("page %d numbered %d", i+1, got[i].Page)
		}
		if !strings.Contains(got[i].Text, want) {
			t.Errorf("page %d = %q, want it to contain %q", i+1, got[i].Text, want)
		}
	}
	// And nothing leaked across: page 1 must not carry page 3's text.
	if strings.Contains(got[0].Text, "PAGE THREE") {
		t.Error("page 1 absorbed page 3's content")
	}
}

// Offsets and page_spans are INDEPENDENT conditions, and conflating them dropped
// every stitched llm-seg page — on the real corpus all 67 span-carrying fragments
// have no offsets at all.
func TestTruePagesHandlesSpansWithoutOffsets(t *testing.T) {
	rows := []fragRow{
		{Page: 1, Ord: 0, Text: "aaa|bbb", StartOff: 0, EndOff: 0,
			PageSpans: `[{"off":0,"page":1},{"off":4,"page":2}]`},
		{Page: 3, Ord: 0, Text: "ccc", StartOff: 0, EndOff: 0},
	}
	got := truePages(rows)
	if len(got) != 3 {
		t.Fatalf("got %d pages, want 3 (1,2,3): %+v", len(got), got)
	}
	if !strings.Contains(got[1].Text, "bbb") {
		t.Errorf("page 2 = %q", got[1].Text)
	}
	if !strings.Contains(got[2].Text, "ccc") {
		t.Errorf("page 3 = %q", got[2].Text)
	}
}

// Overlapping windows must be de-overlapped, or every shared region is repeated —
// which would inflate a page's shingle count and its coverage denominator.
func TestTruePagesDeOverlapsWindows(t *testing.T) {
	full := "ABCDEFGHIJKLMNOP"
	rows := []fragRow{
		{Page: 1, Ord: 0, Text: full[0:10], StartOff: 0, EndOff: 10},
		{Page: 1, Ord: 1, Text: full[6:16], StartOff: 6, EndOff: 16},
	}
	got := truePages(rows)
	if len(got) != 1 {
		t.Fatalf("got %d pages, want 1", len(got))
	}
	if strings.Count(got[0].Text, "GHIJ") != 1 {
		t.Errorf("overlap region repeated: %q", got[0].Text)
	}
}

// A page numbering with a hole in it would shift every alignment after it, so gaps
// are filled with empty pages rather than closed up.
func TestTruePagesKeepsNumberingContiguous(t *testing.T) {
	rows := []fragRow{
		{Page: 1, Text: "one"},
		{Page: 4, Text: "four"},
	}
	got := truePages(rows)
	if len(got) != 4 {
		t.Fatalf("got %d pages, want 4 (1..4): %+v", len(got), got)
	}
	if got[1].Text != "" || got[2].Text != "" {
		t.Error("the gap between page 1 and page 4 was not left empty")
	}
	if got[3].Page != 4 || got[3].Text != "four" {
		t.Errorf("page 4 = %+v", got[3])
	}
}

// The mask must cover a generic REGION, not merely the sampled positions inside it.
// Sampled shingles are spaced about `mod` apart, so unpadded windows of width w only
// abut when mod <= w; padding closes the gaps for any mod.
func TestBuildMaskCoversWholeGenericRegion(t *testing.T) {
	region := strings.Repeat("confidentiality notice belonging to the law office ", 6)
	// The unique material either side must be comfortably longer than the mask
	// padding (mod characters each side, by design) or the padding legitimately eats
	// it and the test measures nothing. With short surroundings the mask covered the
	// whole document, which looked like a bug and was the test's fault.
	doc := FoldPages(pages(deedText + "\n" + region + "\n" + varied(10)))
	generic := map[uint64]bool{}
	for _, h := range Shingles(FoldPages(pages(region)).Body, FoldWidth) {
		if Sampled(h, SampleMod) {
			generic[h] = true
		}
	}
	if len(generic) == 0 {
		t.Skip("no sampled shingle fell inside the region at this modulus")
	}
	m := BuildMask(doc, generic, FoldWidth, SampleMod)
	regionLen := len(FoldPages(pages(region)).Body)
	if maskLen(m) < regionLen/2 {
		t.Errorf("mask covers %d of a %d-char generic region — it is masking positions, not the region",
			maskLen(m), regionLen)
	}
	// It must not swallow the whole document either.
	if maskLen(m) >= len(doc.Body) {
		t.Errorf("mask covers %d of %d chars — it ate the unique material too", maskLen(m), len(doc.Body))
	}
}

func TestBuildMaskWithNoGenericTextIsNil(t *testing.T) {
	doc := FoldPages(pages(deedText))
	if m := BuildMask(doc, nil, FoldWidth, SampleMod); m != nil {
		t.Errorf("mask = %v for an empty generic set", m)
	}
}

// NoMask is the diagnostic for "why was this not reported", so it has to actually
// disable masking rather than merely raise the cutoff a little.
func TestNoMaskDisablesMasking(t *testing.T) {
	var o SimilarOpts
	o.NoMask = true
	if o.genericDocFreq() <= GenericDocFreq {
		t.Errorf("NoMask left the cutoff at %d", o.genericDocFreq())
	}
	o = SimilarOpts{GenericDF: 5}
	if o.genericDocFreq() != 5 {
		t.Errorf("GenericDF override ignored: %d", o.genericDocFreq())
	}
}

// Reporting is gated on matched LENGTH, not coverage: a ratio is meaningless on a
// short document, and a coverage floor high enough to kill signature-block noise
// also discards a paragraph-length quotation inside a long brief.
func TestReportingIsGatedOnMatchedLength(t *testing.T) {
	s := newIndex(t)
	// A quoted paragraph inside a long document: tiny coverage on the long side,
	// but real matched text. It must be reported.
	// Sized to clear MinReportChars, which is 250 FOLDED characters — about 305 raw
	// ones on this kind of text, since folding removes roughly 18% of it. A shorter
	// quotation is deliberately below the floor and needs --min-chars to surface.
	quote := "TOGETHER WITH a non-exclusive easement for ingress and egress over the westerly 25.00 feet thereof, as more particularly described in AF#200807160139, which the parties agree runs with the land and binds their successors and assigns in perpetuity, and shall not be extinguished by merger of the estates however held."
	ingest(t, s, "quote.pdf", quote)
	ingest(t, s, "brief.pdf", filler("argument", 120)+quote+filler("conclusion", 120))
	// Two short notes sharing only a sign-off: high coverage, no substance. It must
	// not be reported.
	ingest(t, s, "note-a.pdf", "Sent from my iPhone. Call about the driveway.")
	ingest(t, s, "note-b.pdf", "Sent from my iPhone. Different subject here.")
	if _, errs := s.SketchAll(FoldWidth, SampleMod); len(errs) > 0 {
		t.Fatal(errs)
	}

	rep, err := s.SimilarIndexed("quote.pdf", SimilarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range rep.Matches {
		if m.Path == "brief.pdf" {
			found = true
			if m.BlockCoverMatch > 0.10 {
				t.Errorf("coverage on the brief side is %.3f — expected it to be tiny, which is why length gates instead", m.BlockCoverMatch)
			}
		}
	}
	if !found {
		t.Error("a paragraph-length quotation inside a long brief was not reported")
	}

	rep, err = s.SimilarIndexed("note-a.pdf", SimilarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rep.Matches {
		if m.Path == "note-b.pdf" {
			t.Errorf("two notes sharing only a sign-off were reported: %+v", m)
		}
	}
}
