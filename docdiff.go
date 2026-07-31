package raglit

import "sort"

// Diffing two transcripts, so the copy-versus-version question is answered from
// evidence instead of from a single score.
//
// `similar` reports one number per document pair and an alignment. That is the
// right shape for "does the corpus already hold this", and the wrong shape for
// "are these the same filing". A whole-document coverage of 0.93 is equally
// consistent with a clean second scan and with a re-recorded instrument whose
// page 3 changed, and the difference between those decides whether a document is
// redundant or is the amendment that governs.
//
// What separates them is WHERE the disagreement is. A bad scan disagrees a
// little on every page — OCR noise is diffuse. A refiling agrees perfectly on
// most pages and then disagrees on one, because that is what was changed. A
// per-page rate makes that shape visible at a glance; a single average destroys
// it, since both cases can average to the same number.
//
// Byte identity is reported first and separately because it ends the question:
// two files with one hash are the same document and no rate needs reading.

// PagePair is one page of A and the page of B it best corresponds to.
type PagePair struct {
	APage int `json:"a_page"`
	// BPage is 0 when nothing in B matches this page at all — a page present in
	// one document and absent from the other, which is the strongest per-page
	// finding available and is invisible in any average.
	BPage int `json:"b_page,omitempty"`
	// Rate is |A∩B| / |A| over the page's distinct shingles: how much of THIS
	// page of A occurs on that page of B. Asymmetric on purpose — a short page
	// wholly inside a long one is 1.0 here and small in reverse.
	Rate float64 `json:"rate"`
	// Identical is exact equality of the two pages' folded text. A rate of 1.0
	// is not the same claim: shingles can be a subset while the text differs in
	// what falls between them.
	Identical bool `json:"identical"`
	AChars    int  `json:"a_chars"`
	BChars    int  `json:"b_chars"`
}

// DocDiff is the full comparison of two transcripts.
type DocDiff struct {
	// SameBytes short-circuits everything below it.
	SameBytes bool `json:"same_bytes"`
	// Match is the whole-document alignment: relation, coverage, blocks, gaps,
	// and the numeric tokens that differ. Reused rather than recomputed so a
	// diff and a `similar` report of the same pair can never disagree.
	Match DocMatch `json:"match"`
	// Pages is one row per page of A, in page order.
	Pages []PagePair `json:"pages"`
	// OnlyInB lists pages of B that no page of A matched — the other half of
	// "what is missing", which a table keyed on A cannot show.
	OnlyInB []int `json:"only_in_b,omitempty"`
	// Shape summarises where the disagreement lives, which is the actual input
	// to the copy-versus-version decision.
	Shape DiffShape `json:"shape"`
}

// DiffShape describes the distribution of disagreement across pages.
type DiffShape struct {
	Pages int `json:"pages"`
	// Clean is pages matching at 1.0, Noisy is pages disagreeing a little
	// (>= NoisyFloor), Divergent is pages disagreeing a lot.
	Clean     int `json:"clean"`
	Noisy     int `json:"noisy"`
	Divergent int `json:"divergent"`
	Missing   int `json:"missing"`
}

// NoisyFloor separates "this page is the same page, read imperfectly" from
// "this page is different".
//
// Set from the corpus rather than by taste: the 2008 lot certification's copy of
// the record of survey — the worst OCR in ardley-v-brannock, reading "LAURENCE
// MOONION" for Clarence Brannock — still holds most of its shingles against the
// clean copy. A floor low enough to call that page divergent would call every
// scanned page divergent, and the distinction this whole file exists to draw
// would be lost.
const NoisyFloor = 0.60

// PageRates computes, for every page of a, the page of b it best corresponds to.
//
// Exact, not sampled. The stored sketches hold a 1-in-N sample, which is right
// for finding candidates across a corpus and wrong here: these numbers are read
// by a person deciding which of two instruments a filing cites, and a rate that
// is usually right is the wrong instrument for that decision.
func PageRates(a, b FoldedText, w int) ([]PagePair, []int) {
	bSets := make([]map[uint64]bool, len(b.Pages))
	for i, p := range b.Pages {
		set := map[uint64]bool{}
		for _, h := range Shingles(b.Body[p.Start:p.End], w) {
			set[h] = true
		}
		bSets[i] = set
	}

	matchedB := map[int]bool{}
	out := make([]PagePair, 0, len(a.Pages))
	for _, ap := range a.Pages {
		aText := a.Body[ap.Start:ap.End]
		aSh := Shingles(aText, w)
		row := PagePair{APage: ap.Page, AChars: ap.End - ap.Start}

		seen := map[uint64]bool{}
		for _, h := range aSh {
			seen[h] = true
		}
		best, bestRate, bestIdx := 0, 0.0, -1
		for i, set := range bSets {
			if len(seen) == 0 || len(set) == 0 {
				continue
			}
			hit := 0
			for h := range seen {
				if set[h] {
					hit++
				}
			}
			if r := float64(hit) / float64(len(seen)); r > bestRate {
				best, bestRate, bestIdx = b.Pages[i].Page, r, i
			}
		}
		row.BPage, row.Rate = best, bestRate
		if bestIdx >= 0 {
			bp := b.Pages[bestIdx]
			row.BChars = bp.End - bp.Start
			row.Identical = aText == b.Body[bp.Start:bp.End]
			matchedB[best] = true
		}
		out = append(out, row)
	}

	var onlyB []int
	for _, p := range b.Pages {
		if !matchedB[p.Page] && p.End > p.Start {
			onlyB = append(onlyB, p.Page)
		}
	}
	sort.Ints(onlyB)
	return out, onlyB
}

// Shape counts how the pages fall out. The counts are the finding: "12 clean, 1
// divergent" is a refiling, "13 noisy" is a second scan, and both can average to
// the same number.
func Shape(pages []PagePair) DiffShape {
	s := DiffShape{Pages: len(pages)}
	for _, p := range pages {
		switch {
		case p.BPage == 0 || p.Rate == 0:
			s.Missing++
		case p.Rate >= 0.999:
			s.Clean++
		case p.Rate >= NoisyFloor:
			s.Noisy++
		default:
			s.Divergent++
		}
	}
	return s
}
