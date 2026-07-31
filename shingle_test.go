package raglit

import (
	"strings"
	"testing"
)

// Hermetic by construction: shingling is local computation, so every test here
// runs offline with no embedder, no OCR and no model — which is the property the
// whole mechanism was chosen for and therefore the property worth pinning.

func TestFoldKeepsLettersAndDigitsOnly(t *testing.T) {
	got := FoldPages([]PageText{{Page: 1, Text: "AF# 200808180120 — N 89°14'32\" W, 25.00 ft."}}).Body
	want := "af200808180120n891432w2500ft"
	if got != want {
		t.Fatalf("fold = %q, want %q", got, want)
	}
}

// The identifiers and measurements on a page of this corpus are the highest-value
// tokens on it, and a fold that canonicalised numbers would report two copies that
// disagree about a distance as agreeing. So: digits survive exactly, and only the
// separators between them go.
func TestFoldPreservesDigitsExactly(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"AF#200808180120", "af200808180120"},
		{"AF 200808180120", "af200808180120"},
		{"AF# 200808180120", "af200808180120"},
		{"25.00 feet", "2500feet"},
		{"25.0 feet", "250feet"}, // NOT equal to 25.00
		{"1,000.50", "100050"},
		{"PL99-0479", "pl990479"},
	} {
		if got := FoldPages([]PageText{{Text: c.in}}).Body; got != c.want {
			t.Errorf("fold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The point of the above: two distances that differ must not fold together.
	a := FoldPages([]PageText{{Text: "25.00 feet"}}).Body
	b := FoldPages([]PageText{{Text: "25.0 feet"}}).Body
	if a == b {
		t.Fatal("25.00 and 25.0 folded to the same text — a distance disagreement would be invisible")
	}
}

// The fold must agree byte-for-byte with kgraph's foldText (quotes.go), or kgraph
// reports a quote present in a document raglit says holds no copy of it. This pins
// the contract from raglit's side; the cases are ones kgraph's own tests cover.
func TestFoldMatchesKgraphContract(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Smart “quotes” and — dashes", "smartquotesanddashes"},
		{"hyphen-\nated", "hyphenated"},
		{"TAB\tand  spaces", "tabandspaces"},
		{"ÉCOLE", "école"},
	} {
		if got := FoldPages([]PageText{{Text: c.in}}).Body; got != c.want {
			t.Errorf("fold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Offs is what turns a folded match back into text a person can read, so a
// mis-sized Offs makes every reported disagreement point at the wrong place.
// Multi-byte runes are the case that breaks it: ToLower can change a rune's
// encoded width.
func TestFoldOffsetsAlignWithBody(t *testing.T) {
	f := FoldPages([]PageText{{Text: "a ÉCOLE b 12"}})
	if len(f.Offs) != len(f.Body) {
		t.Fatalf("Offs has %d entries for %d folded bytes", len(f.Offs), len(f.Body))
	}
	for i := range f.Offs {
		if f.Offs[i] < 0 || f.Offs[i] >= len(f.Raw) {
			t.Fatalf("Offs[%d] = %d out of range for Raw of %d bytes", i, f.Offs[i], len(f.Raw))
		}
	}
	// Monotonic: a later folded byte never came from an earlier source byte.
	for i := 1; i < len(f.Offs); i++ {
		if f.Offs[i] < f.Offs[i-1] {
			t.Fatalf("Offs went backwards at %d: %d then %d", i, f.Offs[i-1], f.Offs[i])
		}
	}
}

// A blank page still gets a FoldedPage, with Start == End. Skipping it would shift
// every page number after it, which is the same contract the transcription
// markdown makes and for the same reason.
func TestFoldKeepsBlankPagesNumbered(t *testing.T) {
	f := FoldPages([]PageText{
		{Page: 1, Text: "alpha"},
		{Page: 2, Text: "   "},
		{Page: 3, Text: "beta"},
	})
	if len(f.Pages) != 3 {
		t.Fatalf("got %d folded pages, want 3", len(f.Pages))
	}
	if f.Pages[1].Start != f.Pages[1].End {
		t.Error("blank page should be empty, not merged away")
	}
	if f.PageAt(f.Pages[2].Start) != 3 {
		t.Errorf("PageAt on page 3's first byte = %d", f.PageAt(f.Pages[2].Start))
	}
}

// Stride MUST be 1. The same passage at a different offset in another document has
// to produce the same shingles, or an exhibit reproduced 300 characters further
// into a filing shares nothing with the standalone copy. This is the property
// raglit's OverlapFragments deliberately does NOT have, which is why it cannot be
// reused here.
func TestShinglesAreShiftInvariant(t *testing.T) {
	body := strings.Repeat("x", 300) + "the operative recorded instrument text goes here and continues"
	shifted := strings.Repeat("y", 617) + "the operative recorded instrument text goes here and continues"
	set := map[uint64]bool{}
	for _, h := range Shingles(FoldPages([]PageText{{Text: body}}).Body, FoldWidth) {
		set[h] = true
	}
	shared := 0
	for _, h := range Shingles(FoldPages([]PageText{{Text: shifted}}).Body, FoldWidth) {
		if set[h] {
			shared++
		}
	}
	// The shared tail folds to 54 characters, so exactly 54-32+1 = 23 shingles lie
	// wholly inside it and every one of them must survive the shift. Asserting the
	// exact count rather than a floor: an off-by-one in the windowing would still
	// clear a floor, and the whole claim is that offsets do not matter.
	if shared != 23 {
		t.Fatalf("%d of 23 shingles survived a shift; stride is not 1", shared)
	}
}

func TestShinglesTooShortYieldsNothing(t *testing.T) {
	if got := Shingles("abc", FoldWidth); got != nil {
		t.Fatalf("got %d shingles from 3 chars", len(got))
	}
	if got := Shingles(strings.Repeat("a", FoldWidth), FoldWidth); len(got) != 1 {
		t.Fatalf("got %d shingles from exactly one window, want 1", len(got))
	}
}

// Sampling must be CONSISTENT: whether a shingle is kept depends only on the
// shingle, never on the set it came from. That is what makes |A∩B| estimable and
// is the whole reason this is a mod-p sample rather than a fixed-K MinHash
// signature — a signature cannot express containment at all.
func TestSampleIsConsistentAcrossSets(t *testing.T) {
	small := FoldPages([]PageText{{Text: strings.Repeat("the recorded instrument ", 8)}})
	large := FoldPages([]PageText{{Text: "prefix material. " + strings.Repeat("the recorded instrument ", 8) + " and forty more pages of unrelated text " + strings.Repeat("filler ", 200)}})
	sampleOf := func(f FoldedText) map[uint64]bool {
		out := map[uint64]bool{}
		for _, h := range Shingles(f.Body, FoldWidth) {
			if Sampled(h, SampleMod) {
				out[h] = true
			}
		}
		return out
	}
	sa, sb := sampleOf(small), sampleOf(large)
	if len(sa) == 0 {
		t.Skip("no sample drawn from the small document at this modulus")
	}
	for h := range sa {
		if !sb[h] {
			t.Fatal("a shingle sampled in the small document was not sampled in the large one — the sample is not consistent, so containment cannot be estimated")
		}
	}
}

// Total is the containment denominator and sampling would make it unrecoverable,
// so it is stored. A page holding no shingles is still emitted, or a re-sketch
// leaves stale rows behind and page numbering goes non-contiguous.
func TestSketchPagesRecordsSizesAndKeepsEmptyPages(t *testing.T) {
	f := FoldPages([]PageText{
		{Page: 1, Text: strings.Repeat("recorded instrument text ", 20)},
		{Page: 2, Text: ""},
	})
	sk := SketchPages(f, FoldWidth, SampleMod)
	if len(sk) != 2 {
		t.Fatalf("got %d sketches, want one per page", len(sk))
	}
	if sk[0].Total == 0 || sk[0].Chars == 0 {
		t.Error("page 1 recorded no size")
	}
	if len(sk[0].Hashes) > sk[0].Total {
		t.Error("more sampled shingles than distinct shingles")
	}
	if sk[1].Page != 2 || sk[1].Total != 0 {
		t.Errorf("blank page 2 = %+v", sk[1])
	}
	for i := 1; i < len(sk[0].Hashes); i++ {
		if sk[0].Hashes[i] < sk[0].Hashes[i-1] {
			t.Fatal("sampled hashes are not sorted")
		}
	}
}

// The strip must fire on a pleading whose numbers are interleaved and NOT on a
// deed that merely wraps onto a line beginning with a digit. The second half is
// the regression: stripping unconditionally deleted "2" from "...lines of Lot\n2
// of said plat", turning a correct transcription into a reported omission.
func TestStripPleadingLineNumbersIsConditional(t *testing.T) {
	var pleading strings.Builder
	for i := 1; i <= 12; i++ {
		pleading.WriteString(itoa(i) + " Defendants shall cooperate as reasonably necessary\n")
	}
	if got := stripPleadingLineNumbers(pleading.String()); strings.Contains(got, "1 Defendants") {
		t.Error("a numbered pleading kept its margin numbers")
	}
	deed := "TOGETHER WITH that portion lying northerly of the north\n" +
		"lines of Lot\n2 of said plat, as recorded in Volume 2 of Plats\n"
	if got := stripPleadingLineNumbers(deed); !strings.Contains(got, "\n2 of said plat") {
		t.Errorf("a wrapped deed line lost its content: %q", got)
	}
}

// Measured on the ardley-v-brannock corpus and recorded on hashShingle: this
// corpus's vision transcription emits margin numbers as a BLOCK, one per line, so
// the regex matches none of them and the strip is a no-op. Pinned because the
// comment claims it, and because the block form is what generic masking has to
// handle instead.
func TestBlockFormLineNumbersAreNotStripped(t *testing.T) {
	var block strings.Builder
	for i := 1; i <= 32; i++ {
		block.WriteString(itoa(i) + "\n")
	}
	block.WriteString("\nIN THE SUPERIOR COURT OF THE STATE OF WASHINGTON\n")
	in := block.String()
	if got := stripPleadingLineNumbers(in); got != in {
		t.Error("block-form margin numbers were stripped; the corpus measurement in the comment no longer holds")
	}
}
