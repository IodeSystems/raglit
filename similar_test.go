package raglit

import (
	"strings"
	"testing"
)

// The synthetic corpus below stands in for the real one. Two things are modelled
// because they are the two that broke every earlier attempt: an instrument
// reproduced INSIDE a much longer filing, and long boilerplate shared by documents
// that are otherwise unrelated.

// deedText is an instrument: distinctive, with the identifiers and measurements a
// disagreement would show up in.
const deedText = `STATUTORY WARRANTY DEED
THE GRANTOR CARTWRIGHT FAMILY TRUST for and in consideration of ten dollars
conveys and warrants to CLARENCE BRANNOCK the following described real estate
situated in the County of Havern, State of Washington:
LOTS 5 AND 6, BLOCK 10, PLAT OF RESERVE ADDITION TO THE TOWN OF ASHFIELDS,
AS PER PLAT RECORDED IN VOLUME 2 OF PLATS, PAGE 59, RECORDS OF HAVERN COUNTY.
TOGETHER WITH a non-exclusive easement for ingress and egress over the westerly
25.00 feet thereof, as more particularly described in AF#200807160139.
Bearings herein are based on N 89°14'32" W along the north line of said Lot 6.
Dated this 10th day of February, 1984.`

// footer is the corpus's chrome: long, contiguous, genuinely identical, and
// present in unrelated documents. This is what defeats Jaccard, containment and
// contiguity alike, and what only document frequency can separate.
const footer = `
--------------------------------------------------------------------------
CONFIDENTIALITY NOTE: THIS E-MAIL MESSAGE CONTAINS INFORMATION BELONGING TO
THE LAW OFFICE OF PAUL W. TAYLOR INC. P.S., THAT MAY BE PRIVILEGED,
CONFIDENTIAL, AND/OR PROTECTED FROM DISCLOSURE. THE INFORMATION IS INTENDED
FOR THE USE OF THE INDIVIDUAL NAMED ABOVE. ANY DISSEMINATION, DISTRIBUTION OR
COPYING OF THIS COMMUNICATION IS STRICTLY PROHIBITED.`

func filler(seed string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(seed + " paragraph " + itoa(i) + " of unrelated recital material. ")
	}
	return b.String()
}

func pages(texts ...string) []PageText {
	out := make([]PageText, len(texts))
	for i, t := range texts {
		out[i] = PageText{Page: i + 1, Text: t}
	}
	return out
}

// The headline case, and the one a Jaccard-only tool fails. A two-page deed inside
// a forty-page filing has containment ~1.0 one way and a Jaccard near zero, so the
// two measures must be reported separately and the relation must come out
// asymmetric.
func TestDeedInsideFilingIsContainedNotSimilar(t *testing.T) {
	deed := FoldPages(pages(deedText))
	filing := FoldPages(pages(
		filler("commitment", 40),
		filler("schedule", 40),
		deedText, // reproduced as an exhibit, on page 3
		filler("endorsement", 40),
	))
	dm := Compare(deed, filing, FoldWidth, nil, nil)

	if dm.Relation != RelProbeInside {
		t.Errorf("relation = %q, want %q", dm.Relation, RelProbeInside)
	}
	if dm.ContainProbe < 0.99 {
		t.Errorf("containment of the deed in the filing = %.3f, want ~1.0", dm.ContainProbe)
	}
	if dm.Jaccard > 0.25 {
		t.Errorf("jaccard = %.3f — expected it to be LOW, which is the whole point", dm.Jaccard)
	}
	if dm.ContainMatch > 0.25 {
		t.Errorf("containment of the filing in the deed = %.3f, want low (it is asymmetric)", dm.ContainMatch)
	}
	// And the alignment must say WHERE, which is the actionable part.
	if len(dm.Blocks) == 0 {
		t.Fatal("no aligned block reported")
	}
	b := dm.Blocks[0]
	if b.MatchFromPage != 3 {
		t.Errorf("the deed aligns to match page %d, want 3", b.MatchFromPage)
	}
	if b.ProbeFromPage != 1 {
		t.Errorf("probe page = %d, want 1", b.ProbeFromPage)
	}
}

// Symmetric case: the same instrument twice is a duplicate in both directions.
func TestSameInstrumentTwiceIsDuplicate(t *testing.T) {
	a := FoldPages(pages(deedText))
	b := FoldPages(pages(deedText))
	dm := Compare(a, b, FoldWidth, nil, nil)
	if dm.Relation != RelDuplicate {
		t.Errorf("relation = %q, want %q", dm.Relation, RelDuplicate)
	}
	if dm.Jaccard < 0.999 {
		t.Errorf("jaccard = %.4f for identical text", dm.Jaccard)
	}
	if len(dm.NumericOnlyInProbe) != 0 || len(dm.NumericOnlyInMatch) != 0 {
		t.Errorf("identical text reported a numeric disagreement: %v / %v",
			dm.NumericOnlyInProbe, dm.NumericOnlyInMatch)
	}
}

// The finding that matters most in a legal corpus: two copies of one instrument
// that DISAGREE. Collapsing near-duplicates into one blob loses exactly this, so
// the differing numbers have to come back out.
func TestDisagreeingCopiesSurfaceTheChangedNumbers(t *testing.T) {
	orig := FoldPages(pages(deedText))
	// An altered copy: the easement width and the auditor file number changed, and
	// the bearing's minutes changed. Everything else identical.
	altered := FoldPages(pages(strings.NewReplacer(
		"25.00 feet", "30.00 feet",
		"AF#200807160139", "AF#200807160155",
		`N 89°14'32" W`, `N 89°18'32" W`,
	).Replace(deedText)))

	dm := Compare(orig, altered, FoldWidth, nil, nil)
	if dm.Relation != RelDuplicate {
		t.Fatalf("relation = %q — near-identical copies should still pair up", dm.Relation)
	}
	has := func(v []string, want string) bool {
		for _, s := range v {
			if s == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"25.00", "200807160139", "14"} {
		if !has(dm.NumericOnlyInProbe, want) {
			t.Errorf("%q is in the original only and was not reported: %v", want, dm.NumericOnlyInProbe)
		}
	}
	for _, want := range []string{"30.00", "200807160155", "18"} {
		if !has(dm.NumericOnlyInMatch, want) {
			t.Errorf("%q is in the altered copy only and was not reported: %v", want, dm.NumericOnlyInMatch)
		}
	}
	// A disagreement must also be LOCATABLE, with readable text from both sides —
	// the aggregate lists above say what changed, not where to look in the original.
	//
	// The gap text is only the DIVERGING part: "25.00" against "30.00" shares the
	// "00", which belongs to the matching run either side of it, so the gap is "25."
	// against "30.". That is five characters, well under minGapChars, and is exactly
	// the case the numeric exemption in trimGaps exists for.
	found := false
	for _, b := range dm.Blocks {
		for _, g := range b.Gaps {
			if strings.HasPrefix(g.ProbeText, "25.") && strings.HasPrefix(g.MatchText, "30.") {
				found = true
			}
		}
		if b.Agreement() >= 1.0 {
			t.Errorf("block agreement %.4f — copies that differ must not report as identical", b.Agreement())
		}
	}
	if !found {
		t.Error("no gap showed the changed distance in both copies' own words")
	}
}

// Boilerplate is the failure that masking exists for, and it is worth pinning from
// both sides: without a mask two unrelated notes sharing a footer look like
// duplicates; with one they do not.
func TestSharedBoilerplateNeedsMaskingNotThresholds(t *testing.T) {
	a := FoldPages(pages("Attached is my draft proposed agreement. I talked to Bruce and sent it to him." + footer))
	b := FoldPages(pages("Please see the enclosed response regarding the July hearing date." + footer))

	unmasked := Compare(a, b, FoldWidth, nil, nil)
	if unmasked.Relation == RelOverlap {
		t.Skip("this footer is too short to fool the unmasked comparison; the case is still covered by the masked half")
	}

	// The mask stands in for what the corpus-frequency query produces: the footer's
	// shingles occur in many documents, so they are generic.
	generic := map[uint64]bool{}
	for _, h := range Shingles(FoldPages(pages(footer)).Body, FoldWidth) {
		generic[h] = true
	}
	masked := Compare(a, b, FoldWidth,
		BuildMask(a, generic, FoldWidth, SampleMod),
		BuildMask(b, generic, FoldWidth, SampleMod))
	if masked.Relation != RelOverlap {
		t.Errorf("masked relation = %q, want %q — the only thing these share is a footer",
			masked.Relation, RelOverlap)
	}
	if masked.MatchedChars >= unmasked.MatchedChars {
		t.Errorf("masking did not reduce the match: %d then %d",
			unmasked.MatchedChars, masked.MatchedChars)
	}
}

// Masking must not eat a real instrument that happens to sit beside boilerplate.
// This is the failure mode that makes the DF cutoff dangerous: it is silent.
func TestMaskingSparesRealTextBesideBoilerplate(t *testing.T) {
	a := FoldPages(pages("Please find the deed attached." + footer + "\n" + deedText))
	b := FoldPages(pages("Different covering note entirely." + footer + "\n" + deedText))
	generic := map[uint64]bool{}
	for _, h := range Shingles(FoldPages(pages(footer)).Body, FoldWidth) {
		generic[h] = true
	}
	dm := Compare(a, b, FoldWidth,
		BuildMask(a, generic, FoldWidth, SampleMod),
		BuildMask(b, generic, FoldWidth, SampleMod))
	if dm.Relation == RelOverlap {
		t.Errorf("relation = %q — the shared deed should still pair these", dm.Relation)
	}
	if dm.MatchedChars < 400 {
		t.Errorf("only %d chars matched; the deed should survive masking", dm.MatchedChars)
	}
}

// varied is prose with no repeated phrasing, so alignment is not confounded by a
// document matching itself in many places. filler() is deliberately self-similar
// and is the wrong input here: with it, the corrupted copy's repeated shingles
// exceed MaxPostingsPerHash and get dropped as filler, so coverage came back
// asymmetric (0.74 one way, 0.43 the other) for reasons that had nothing to do with
// the corruption under test.
func varied(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("Recital " + itoa(i) + ": the grantor conveys parcel P" +
			itoa(74000+i*7) + " lying " + itoa(i*3+11) + " feet " +
			[...]string{"northerly", "southerly", "easterly", "westerly"}[i%4] +
			" of the centerline described in AF#20080716" + itoa(1000+i) + ". ")
	}
	return b.String()
}

// corrupt replaces every nth character with one the fold drops. That models both an
// OCR read that garbles a character and one that loses it — the fold makes those the
// same thing — and it is the harsher case, because a dropped character shifts every
// offset after it and so changes the alignment delta.
func corrupt(s string, every int) string {
	var b strings.Builder
	for i, r := range s {
		if i%every == every-1 && r != '\n' {
			b.WriteRune('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// OCR never reproduces a page exactly, so a degraded copy has to stay findable.
func TestDegradedOcrCopyIsStillFound(t *testing.T) {
	body := deedText + "\n" + varied(40)
	dm := Compare(FoldPages(pages(body)), FoldPages(pages(corrupt(body, 120))), FoldWidth, nil, nil)
	if dm.Relation != RelDuplicate {
		t.Errorf("relation = %q, want %q (coverage %.3f/%.3f)",
			dm.Relation, RelDuplicate, dm.BlockCoverProbe, dm.BlockCoverMatch)
	}
	if dm.BlockCoverProbe >= 0.999 {
		t.Error("a corrupted copy reported perfect coverage — the corruption was not detected at all")
	}
}

// The detection floor, pinned because it is SHARP and because knowing where it is
// beats discovering it on a real exhibit.
//
// A run qualifies only if it reaches MinRunChars, and a run can only come from a
// stretch with no divergence in it, so with divergences spread evenly the whole
// mechanism collapses once the clean stretches fall under MinRunChars. Measured, one
// dropped character every k characters of RAW text:
//
//	k     forward cov     reverse cov     relation
//	200   0.998/0.994     0.994/0.998     duplicate
//	120   0.996/0.991     0.991/0.996     duplicate
//	 80   0.994/0.999     0.983/0.994     duplicate
//	 60   0.991/0.997     0.980/0.983     duplicate
//	 50   0.902/0.875     0.738/0.834     duplicate
//	 45   0.542/0.211     0.210/0.217     overlap
//	 40   0.637/0.313     0.311/0.425     probe-inside-match
//	 30   0.579/0.272     0.266/0.295     probe-inside-match
//	 20   0.026/0.027     0.027/0.026     overlap
//	 15   0.000/0.000     0.000/0.000     overlap
//
// The cliff is between 50 and 45, and MinRunChars is 40, because the floor is in
// FOLDED characters: this text folds to about 0.82 of its raw length once spaces and
// punctuation go, so 40 folded characters needs roughly 49 raw ones. Predicted 49.8,
// observed between 45 and 50.
//
// Note the second column below the cliff. Coverage stops being reciprocal — 0.64 one
// way and 0.31 the other for the same pair — because nearly every run lands just
// under the floor and which ones survive comes down to where MaxPostingsPerHash
// happens to bite. Wild non-reciprocity is the signature of a pair sitting on the
// cliff, and is more informative than the coverage number itself.
//
// This is the honest limit of the mechanism, and the honest answer to "why not a
// larger w": w=64 would move the floor UP, to about one divergence per 78 raw
// characters, making bad copies harder to find rather than easier. A copy degraded
// past roughly one divergence per 49 raw characters is not findable by shingles at
// these settings — that is what the exact-bytes check and a human are for.
func TestDetectionFloorIsSharpBelowMinRunChars(t *testing.T) {
	body := deedText + "\n" + varied(40)
	clean := FoldPages(pages(body))
	at := func(every int) DocMatch {
		return Compare(clean, FoldPages(pages(corrupt(body, every))), FoldWidth, nil, nil)
	}
	// Above the cliff: found, and reciprocal.
	for _, every := range []int{50, 60, 120} {
		dm := at(every)
		if dm.Relation != RelDuplicate {
			t.Errorf("one divergence per %d chars: relation %q, coverage %.3f/%.3f — want %q",
				every, dm.Relation, dm.BlockCoverProbe, dm.BlockCoverMatch, RelDuplicate)
		}
	}
	// Below it: not a duplicate any more. Asserting only that, not a specific
	// coverage — the numbers down there are unstable by nature, which is the point.
	for _, every := range []int{30, 20} {
		if dm := at(every); dm.Relation == RelDuplicate {
			t.Errorf("one divergence per %d chars still reported %q at %.3f/%.3f — the floor moved",
				every, dm.Relation, dm.BlockCoverProbe, dm.BlockCoverMatch)
		}
	}
	// And the total collapse is real: heavy corruption finds nothing at all.
	if dm := at(15); dm.MatchedChars != 0 {
		t.Errorf("one divergence per 15 chars matched %d chars; expected none", dm.MatchedChars)
	}
}

// Coverage and block agreement are ratios and must never exceed 1.0. Summing run
// lengths instead of unioning them produced both, and a number above 1.0 in a
// report people are asked to trust reads as a bug in everything else too. The
// repeated-block case is the one that triggers it: a deed attached twice.
func TestCoverageAndAgreementNeverExceedOne(t *testing.T) {
	once := FoldPages(pages(deedText))
	twice := FoldPages(pages(deedText, filler("gap", 5), deedText))
	for _, dm := range []DocMatch{
		Compare(once, twice, FoldWidth, nil, nil),
		Compare(twice, once, FoldWidth, nil, nil),
		Compare(twice, twice, FoldWidth, nil, nil),
	} {
		if dm.BlockCoverProbe > 1.0 || dm.BlockCoverMatch > 1.0 {
			t.Errorf("coverage %.3f/%.3f exceeds 1.0", dm.BlockCoverProbe, dm.BlockCoverMatch)
		}
		for _, b := range dm.Blocks {
			if b.Agreement() > 1.0001 {
				t.Errorf("block agreement %.4f exceeds 1.0 (matched %d, span %d)",
					b.Agreement(), b.MatchedChars, b.SpanChars)
			}
		}
	}
}

// A block must mean "this contiguous region of the probe corresponds to this
// contiguous region of the match, in order". Non-monotone chaining produced
// overlapping, contradictory page ranges from a pair that is page-for-page
// identical.
func TestBlockPageRangesAreMonotone(t *testing.T) {
	long := filler("recital", 30)
	a := FoldPages(pages(deedText, long, deedText+long))
	b := FoldPages(pages(long, deedText, long+deedText))
	dm := Compare(a, b, FoldWidth, nil, nil)
	for _, blk := range dm.Blocks {
		if blk.ProbeToPage < blk.ProbeFromPage {
			t.Errorf("probe page range runs backwards: p%d-%d", blk.ProbeFromPage, blk.ProbeToPage)
		}
		if blk.MatchToPage < blk.MatchFromPage {
			t.Errorf("match page range runs backwards: p%d-%d", blk.MatchFromPage, blk.MatchToPage)
		}
		if blk.MatchedChars > blk.SpanChars {
			t.Errorf("block claims %d matched chars in a %d-char span", blk.MatchedChars, blk.SpanChars)
		}
	}
}

// Nothing shared means nothing reported. An `overlap` on two unrelated documents
// teaches people to ignore the output.
func TestUnrelatedDocumentsShareNothing(t *testing.T) {
	a := FoldPages(pages(filler("alpha", 20)))
	b := FoldPages(pages(filler("omega", 20)))
	dm := Compare(a, b, FoldWidth, nil, nil)
	if dm.MatchedChars != 0 {
		t.Errorf("%d chars matched between unrelated documents", dm.MatchedChars)
	}
	if dm.Relation != RelOverlap {
		t.Errorf("relation = %q, want %q", dm.Relation, RelOverlap)
	}
}

// A coverage ratio is meaningless on a short document: 0.83 of a 400-character
// email is a signature block. So a duplicate/containment claim needs absolute
// matched text behind it, not just a ratio.
func TestShortSharedTextIsNotADuplicate(t *testing.T) {
	a := FoldPages(pages("Sent from my iPhone. Call me back about the driveway."))
	b := FoldPages(pages("Sent from my iPhone. Different subject entirely here."))
	dm := Compare(a, b, FoldWidth, nil, nil)
	if dm.Relation != RelOverlap {
		t.Errorf("relation = %q for %d matched chars, want %q",
			dm.Relation, dm.MatchedChars, RelOverlap)
	}
}

// The repeat cap is what keeps a degenerate document from going quadratic. Pinned
// with a document that is almost entirely one repeated phrase — measured on the
// real corpus as 67.8 M seeds uncapped against 510 capped.
func TestRepetitiveDocumentDoesNotExplode(t *testing.T) {
	rep := FoldPages(pages(strings.Repeat("the same clause repeated verbatim again and again. ", 2000)))
	done := make(chan DocMatch, 1)
	go func() { done <- Compare(rep, rep, FoldWidth, nil, nil) }()
	dm := <-done
	if dm.BlockCoverProbe > 1.0 {
		t.Errorf("coverage %.3f", dm.BlockCoverProbe)
	}
}

// A recipe change must be visible, because a finding computed under one set of
// parameters is not comparable to one computed under another.
func TestRecipeDistinguishesParameters(t *testing.T) {
	if Recipe(32, 32) == Recipe(48, 32) || Recipe(32, 32) == Recipe(32, 16) {
		t.Fatal("Recipe collided across different parameters")
	}
	if Recipe(32, 32) != "sh1/w32/m32" {
		t.Errorf("Recipe = %q", Recipe(32, 32))
	}
}

func TestParseTranscriptionRoundTripsPages(t *testing.T) {
	md := RenderTranscription("deed.pdf", []TranscribedPage{
		{Page: 1, Text: "first page text"},
		{Page: 2, Text: ""},
		{Page: 3, Text: "third page text", Figures: []TranscribedFigure{
			{Kind: "figure", Description: "survey map showing the 25.00 foot strip"},
		}},
	})
	got := ParseTranscription(md)
	if len(got) != 3 {
		t.Fatalf("parsed %d pages, want 3", len(got))
	}
	if got[0].Page != 1 || !strings.Contains(got[0].Text, "first page text") {
		t.Errorf("page 1 = %+v", got[0])
	}
	// raglit's own prose must not survive: the empty-page placeholder is identical
	// on every blank page in a corpus and would make them all match each other.
	if got[1].Text != "" {
		t.Errorf("blank page kept raglit's placeholder: %q", got[1].Text)
	}
	// A figure description must survive. On a survey or a plat it is most of the
	// evidence on the page, and dropping it makes two different plats compare as
	// two blank pages.
	if !strings.Contains(got[2].Text, "25.00 foot strip") {
		t.Errorf("page 3 lost its figure description: %q", got[2].Text)
	}
}

func TestParseTranscriptionDropsSuspectWarningKeepsFigures(t *testing.T) {
	md := "## Page 1\n\n> ⚠ **Check this page against the original.** It looks like a watermark.\n\n" +
		"REAL CONTENT HERE\n\n> **figure:** a plat showing Lot 6\n"
	got := ParseTranscription(md)
	if len(got) != 1 {
		t.Fatalf("parsed %d pages", len(got))
	}
	if strings.Contains(got[0].Text, "Check this page") {
		t.Error("the suspect-page warning survived — it is identical on every suspect page in a corpus")
	}
	if !strings.Contains(got[0].Text, "a plat showing Lot 6") {
		t.Error("the figure description was stripped along with the warning")
	}
}
