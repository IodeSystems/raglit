package raglit

import (
	"regexp"
	"sort"
	"strings"
)

// Near-duplicate and containment detection.
//
// The corpus this exists for is a litigation record in which the SAME recorded
// instrument genuinely arrives many times: standalone from the county auditor, as
// an exhibit inside a declaration, reproduced in a title commitment, photographed
// by a client, re-exported to PDF by a paralegal. A web upload endpoint now takes
// files from clients, so the rate is about to rise. Two questions have to be
// answered separately and are constantly conflated:
//
//	SIMILARITY  |A∩B| / |A∪B|   symmetric. "Are these the same document?"
//	CONTAINMENT |A∩B| / |A|     asymmetric. "Is A inside B?"
//
// Reporting only similarity misses every appended exhibit: a 2-page deed wholly
// inside a 40-page title commitment has containment 1.0 one way and a Jaccard of
// about 0.05, so a Jaccard threshold anywhere useful calls them unrelated. Both
// are reported, in both directions, and neither is allowed to stand in for the
// other.
//
// A THIRD measure carries most of the weight, and it is neither of those. Two
// unrelated recorded deeds from one county share their statutory form, notary
// block and auditor's certificate — enough shared text that raw set measures
// cannot separate "the same instrument twice" from "two documents on the same
// county's forms". What separates them is CONTIGUITY: shared boilerplate is
// scattered short runs, while a real copy is one long run. So the primary
// evidence is the ALIGNMENT — maximal runs of identical text, chained into blocks
// — and BlockCoverage, the fraction of a document covered by runs long enough to
// be a passage rather than a phrase. That is also the output the caller actually
// wanted: not a score, but "pages 12-14 of X are pages 1-3 of Y".
//
// And the finding that matters most is DISAGREEMENT. "We hold this instrument
// three times and they agree" is corroboration. "They disagree" is either an
// altered exhibit or a different version filed, and in this matter one exhibit's
// OCR was found to be wholly fabricated. So near-duplicates are never collapsed
// into one blob: an aligned pair reports its gaps, and the numeric tokens that
// differ across them, because a distance or an auditor file number that changed
// between two copies of one deed is the whole point.

const (
	// MinRunChars is the shortest run of identical text counted as a match, in
	// folded characters. 40 characters is six or seven words.
	//
	// This is the phrase-noise cutoff. Swept 1/20/40/80/200 on three pairs chosen
	// to span the difficulty:
	//
	//	                                        1      20     40     80    200
	//	Graham declaration vs its own email
	//	  attachment (byte-identical files)     .930   .927   .927   .927  .927
	//	two DIFFERENT Havern deeds, 1993 QCD
	//	  vs 2008 deed (same county forms)      .307   .301   .259   .033  .000
	//	1946 survey vs 1992 reconveyance
	//	  (unrelated)                           .010   .010   .000   .000  .000
	//
	// The shape is what decides it: a true duplicate's coverage does not move at
	// all across the whole sweep, so raising the floor costs nothing there, while
	// unrelated documents go to exactly zero by 40. The sensitive case is the
	// middle one — two different instruments on the same county's forms — and 40
	// leaves it at 0.26, less than half of DuplicateThreshold.
	//
	// Not 80, which would take that middle case to 0.03: it would also discard a
	// genuine paragraph-length quotation, which is a finding this corpus turns on.
	// Report-level noise is controlled by MinReportChars instead, which can be
	// raised without making short real matches invisible.
	MinRunChars = 40

	// blockSlack is how much unmatched text two runs may have between them and
	// still be chained into one block, in folded characters.
	//
	// A PRESENTATION parameter and nothing more — measured to have no effect on any
	// score. Runs break at every disagreement and OCR disagrees constantly, so a
	// page that is plainly one correspondence arrives as dozens of runs. Swept
	// 0/100/300/600/1200/5000 on the hardest real pair (the 2008 record of survey
	// against the copy inside the lot certification, whose OCR is substantially
	// garbled): coverage was 0.589/0.082 at EVERY setting, while the block count
	// went 44/28/21/15/13/9.
	//
	// 600 is where merging saturates: 600 and 1200 produce a byte-identical top
	// block (1,783 matched chars, 0.56 agreement), so growing it further only
	// glues distinct regions together without adding information.
	blockSlack = 600

	// MaxPostingsPerHash caps how many times a shingle may occur in the compared
	// document before it is ignored as an anchor.
	//
	// This is not a tuning knob, it is what keeps the alignment from going
	// quadratic on a degenerate document. A shingle occurring thousands of times is
	// repetitive filler — a run of dot leaders, a table of blank cells, a failed OCR
	// emitting one phrase over and over — and it cannot anchor a unique alignment,
	// only multiply seeds.
	//
	// Measured over every document on this corpus: the worst
	// (200806030039-2008-06-predecessor-survey-lotB.pdf, whose transcription is
	// largely one repeated string) would generate 67,829,506 seeds against itself
	// uncapped, and 510 capped. Five orders of magnitude, from one document. Same
	// reasoning as repeat masking in a sequence aligner.
	MaxPostingsPerHash = 24

	// DuplicateThreshold is the block coverage at which a document is called a copy
	// rather than an overlap.
	//
	// The measured separation on this corpus is wide. Two DIFFERENT recorded
	// instruments on the same county's forms reach 0.26; the worst genuinely
	// byte-identical pair reaches 0.92 (the Graham declaration against its own email
	// attachment — under 1.00 only because generic-text masking removes the same
	// passages from both sides). So anything from about 0.3 to 0.9 separates those.
	//
	// 0.55 is set by the one case that falls INSIDE that gap, and it is the case the
	// feature exists for: the operative 2008 record of survey is reproduced on page 4
	// of the 2008 lot certification, and aligns at 0.589 rather than ~0.95 because
	// the certification's OCR is substantially garbled ("LAURENCE MOONION" for
	// Clarence Brannock, "SEATTLE TACOMA SHORE" for Seattle Lake Shore). A threshold
	// of 0.60 called that pair a mere `overlap`, missing a real sub-document
	// containment by 0.011.
	//
	// Setting a threshold from a single example is normally how a measurement gets
	// fitted to noise. It is defensible here because the example is the DEGRADED-COPY
	// case rather than an outlier — degraded copies are the population this tool
	// serves — and because 0.55 is still more than twice the level two different
	// instruments reach. The error direction is deliberate: a false `duplicate`
	// costs one human comparison, a missed one lets a second copy of an instrument
	// enter the record unnoticed.
	DuplicateThreshold = 0.55
)

// Relation names how a pair of documents overlap. Deliberately four values and
// not a score: the caller is a triage flow that has to DO something different in
// each case, and "0.63" does not say which.
type Relation string

const (
	// RelIdentical — the two files are byte-for-byte the same. Not a shingle
	// finding at all: it comes from documents.content_hash, which ingest already
	// records for its own re-index skip.
	//
	// Kept separate from RelDuplicate because it is a strictly stronger claim and
	// because the shingle scores UNDERSTATE it. Three byte-identical pairs exist on
	// this corpus and none scored 1.00 — the Graham declaration against its own
	// email attachment came back 0.93/0.92, because generic-text masking removes
	// the same passages from both sides and coverage is measured over what is left.
	// A triage flow told "0.93 similar" about two identical files will ask a human
	// to compare them; told "identical", it can act.
	RelIdentical Relation = "identical"
	// RelDuplicate — each document is substantially the whole of the other.
	RelDuplicate Relation = "duplicate"
	// RelProbeInside — the probe is contained in the match. The upload is an
	// instrument we already hold as part of a larger filing.
	RelProbeInside Relation = "probe-inside-match"
	// RelMatchInside — the match is contained in the probe. The upload is a larger
	// filing that swallows something we already hold standalone; the held copy is
	// probably the better evidence.
	RelMatchInside Relation = "match-inside-probe"
	// RelOverlap — they share passages without either containing the other.
	// Quotation, a shared exhibit, or a partial reproduction.
	RelOverlap Relation = "overlap"
)

// Run is one maximal span of identical text: A[AStart:AEnd) equals B[BStart:BEnd)
// byte for byte in folded form, and the two spans are the same length.
type Run struct {
	AStart, AEnd int
	BStart, BEnd int
}

// Len is the run's length in folded characters.
func (r Run) Len() int { return r.AEnd - r.AStart }

// Block is a chained group of runs — one reportable aligned region, with the page
// ranges that make it actionable.
type Block struct {
	ProbeFromPage int `json:"probe_from_page"`
	ProbeToPage   int `json:"probe_to_page"`
	MatchFromPage int `json:"match_from_page"`
	MatchToPage   int `json:"match_to_page"`
	// Runs is how many separate identical spans the block was chained from. 1
	// means the region is identical throughout; a high count on a short block
	// means the two copies differ constantly, which is itself the finding.
	Runs int `json:"runs"`
	// MatchedChars is folded characters proved identical inside the block;
	// SpanChars is the block's extent on the probe side. Agreement is their ratio.
	MatchedChars int `json:"matched_chars"`
	SpanChars    int `json:"span_chars"`
	// Gaps are the disagreements inside the block, largest first, capped.
	Gaps []Gap `json:"gaps,omitempty"`

	aStart, aEnd int
	bStart, bEnd int
}

// Agreement is the fraction of the block's extent proved identical. Below 1.0 the
// two copies of this passage are not the same text, and Gaps says where.
func (b Block) Agreement() float64 {
	if b.SpanChars == 0 {
		return 0
	}
	return float64(b.MatchedChars) / float64(b.SpanChars)
}

// Gap is one disagreement inside an aligned block: the unmatched text between two
// runs, from both sides, as a person would read it.
type Gap struct {
	ProbePage int    `json:"probe_page"`
	MatchPage int    `json:"match_page"`
	ProbeText string `json:"probe_text"`
	MatchText string `json:"match_text"`
}

// DocMatch is one document that shares text with the probe.
type DocMatch struct {
	Path  string `json:"path"`
	Title string `json:"title,omitempty"`

	Relation Relation `json:"relation"`

	// Jaccard is |A∩B|/|A∪B| over distinct shingles: "are these the same
	// document". Exact, not estimated.
	Jaccard float64 `json:"jaccard"`
	// ContainProbe is |A∩B|/|A| — how much of the PROBE occurs in this document.
	// ContainMatch is the reverse. Asymmetric on purpose; a single number here
	// cannot express "wholly inside".
	ContainProbe float64 `json:"contain_probe"`
	ContainMatch float64 `json:"contain_match"`

	// BlockCoverProbe / BlockCoverMatch are the fraction of each side covered by
	// runs of at least MinRunChars. These are the numbers Relation is decided on,
	// because they are the ones shared boilerplate cannot inflate.
	BlockCoverProbe float64 `json:"block_cover_probe"`
	BlockCoverMatch float64 `json:"block_cover_match"`

	SharedShingles int `json:"shared_shingles"`
	ProbeShingles  int `json:"probe_shingles"`
	MatchShingles  int `json:"match_shingles"`

	// GenericChars is how much of the PROBE was excluded as corpus-generic (an
	// email footer, a caption repeated across unrelated filings). Reported rather
	// than applied silently: masking is the one step that can hide a real
	// duplicate, so when a document is mostly generic the person reading the
	// result has to be able to see that is why the score is low.
	GenericChars int `json:"generic_chars,omitempty"`

	// MatchedChars is the total folded characters proved identical, counting
	// overlaps once. Coverage alone cannot be acted on for a short document: 0.83
	// of a 400-character email is a signature block, and 0.83 of a deed is a deed.
	MatchedChars int `json:"matched_chars"`

	Blocks []Block `json:"blocks,omitempty"`

	// NumericOnlyInProbe / NumericOnlyInMatch are numeric tokens (auditor file
	// numbers, distances, bearings, dollar amounts, dates) that appear inside the
	// aligned region on ONE side only.
	//
	// This is the headline finding for a legal corpus and it is cheap. Two copies
	// of one deed that align at 0.97 and disagree about "25.00" versus "30.00" are
	// not a housekeeping problem; in this matter there is an open question about
	// which of two 25-foot strips a plat depicts, and one exhibit's OCR was found
	// fabricated outright. A digits-level diff over the aligned span puts that in
	// front of a person instead of averaging it into a score.
	NumericOnlyInProbe []string `json:"numeric_only_in_probe,omitempty"`
	NumericOnlyInMatch []string `json:"numeric_only_in_match,omitempty"`
}

// SimilarReport is what `raglit similar` returns. Shaped to be consumed
// programmatically first — the upload triage flow is the caller that matters, and
// it needs to branch on Relation and read page ranges, not parse a table.
type SimilarReport struct {
	// Probe is the path compared. Indexed is whether it was read from the index
	// (rather than extracted from a file on disk), because that decides whether
	// the probe's own text has been through this index's OCR or a different run of
	// it — two reads of one scan differ, and a lower score against an otherwise
	// identical document is explained by that.
	Probe   string `json:"probe"`
	Indexed bool   `json:"indexed"`
	// Source says where the probe's TEXT came from when it was not the index:
	// "extracted" (read locally, no OCR) or "transcription:<file>" (a sidecar
	// written by an earlier ingest). Recorded because a lower score against an
	// otherwise identical document is usually explained by this — two OCR reads of
	// one scan differ, and comparing an extraction against an OCR'd copy is not
	// comparing like with like.
	Source string `json:"source,omitempty"`
	Pages  int    `json:"pages"`
	Chars  int    `json:"chars"`
	// Shingled is the probe's distinct shingle count. Zero means the probe holds
	// less text than one shingle — a blank scan, or a page that is pure figure —
	// and NO match can be reported. Distinguishing that from "nothing similar
	// found" matters: the first is "we cannot tell", the second is "we checked".
	Shingled int `json:"shingled"`
	// Exact names indexed documents whose SOURCE BYTES are the same as the probe's,
	// path -> title. Independent of every score here and stronger than all of them;
	// present even when the probe's text could not be read at all.
	Exact map[string]string `json:"exact,omitempty"`
	// GenericChars is how much of the probe was masked as corpus-generic. A probe
	// that is almost entirely generic (a bare email footer, a cover sheet) will
	// report no matches, and this is what says why.
	GenericChars int        `json:"generic_chars,omitempty"`
	Matches      []DocMatch `json:"matches"`
	// Recipe is the fold/shingle parameters the report was produced under, so a
	// stored finding can be told from one computed under different settings.
	Recipe string `json:"recipe"`
}

// Recipe identifies the comparison parameters. Mirrors documents.frag_recipe: a
// parameter change should mark exactly what it affects, and a finding computed
// under one recipe is not comparable to one computed under another.
func Recipe(w, mod int) string {
	return "sh1/w" + itoa(w) + "/m" + itoa(mod)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Mask marks regions of a document that are CORPUS-GENERIC — text that recurs
// across unrelated documents and therefore says nothing about whether two
// documents are copies of each other.
//
// This is the single most important correction measured on real data, and the
// failure it fixes was not the one expected. Legal boilerplate (statutory deed
// forms, notary blocks) turned out to be survivable: it produces scattered runs
// that a 40-character floor and a coverage measure already discount. What broke
// the tool was EMAIL CHROME. Two entirely different one-paragraph emails from the
// same lawyer shared their header template and a 450-character confidentiality
// notice — 593 of 800 folded characters — and were reported as `duplicate` at
// 0.75/0.69 coverage.
//
// Measured on the all-pairs audit: 774 duplicate-or-containment findings with no
// masking, 57 with it, and the same 5 known-true pairs present in both. A triage
// tool that cries duplicate 774 times on a 233-document corpus is a tool nobody
// runs twice. The specific pair above went from `duplicate` 0.75/0.69 to `overlap`
// 0.09/0.09 with 981 of its characters masked.
//
// Neither Jaccard, containment, nor contiguity can separate that case: the shared
// text is long, contiguous, and genuinely identical. Only its DOCUMENT FREQUENCY
// distinguishes it. So generic regions are removed from the alignment.
//
// Intervals are in FOLDED offsets, half-open, sorted and non-overlapping.
type Mask []iv

// Compare is the exact pairwise comparison: no sampling, no estimate.
//
// Candidate generation samples (see shingle.go), because probing an index must
// not read every document. Scoring does not, because these numbers end up in a
// human decision about which copy of an instrument a filing cites, and an
// estimate that is usually right is the wrong tool for that. At this corpus size
// exactness is nearly free: the whole 233-document index folds to 1.59 MB, so a
// pair costs two shingle passes over a few tens of kilobytes. The full all-pairs
// audit of the corpus — 233 probes, each rescoring its candidates exactly — runs in
// 27 seconds.
//
// One asymmetry to know about, because it looks like a bug and is not:
// MatchedChars, BlockCoverProbe and the reported blocks are all measured on the
// PROBE side, so Compare(a,b) and Compare(b,a) do not report the same matched
// length. A passage of the smaller document that recurs at nine places in the
// larger one is 383 characters measured on the small side and 3,612 on the large
// one; both are true statements about different questions. Jaccard is symmetric,
// the containments are deliberately not, and the blocks answer "where in the
// PROBE", which is what a triage caller asked.
// probeMask and matchMask may be nil, which is how the pure two-document tests
// exercise the scoring without a corpus to derive frequencies from.
func Compare(probe, match FoldedText, w int, probeMask, matchMask Mask) DocMatch {
	var dm DocMatch
	sa := Shingles(probe.Body, w)
	sb := Shingles(match.Body, w)

	// Distinct-set measures. Sets are built from the position lists rather than
	// the other way round so the same hashing pass serves both the scores and the
	// alignment.
	setA := map[uint64]bool{}
	for _, h := range sa {
		setA[h] = true
	}
	posB := map[uint64][]int{}
	setB := map[uint64]bool{}
	over := map[uint64]bool{} // hashes past MaxPostingsPerHash: filler, not an anchor
	for i, h := range sb {
		setB[h] = true
		if over[h] {
			continue
		}
		if len(posB[h]) == MaxPostingsPerHash {
			// Drop what was collected rather than keeping an arbitrary prefix: a
			// partial posting list produces alignments at whichever occurrences
			// happened to come first, which is not reproducible across a re-index.
			delete(posB, h)
			over[h] = true
			continue
		}
		posB[h] = append(posB[h], i)
	}
	shared := 0
	for h := range setA {
		if setB[h] {
			shared++
		}
	}
	dm.SharedShingles, dm.ProbeShingles, dm.MatchShingles = shared, len(setA), len(setB)
	if union := len(setA) + len(setB) - shared; union > 0 {
		dm.Jaccard = float64(shared) / float64(union)
	}
	if len(setA) > 0 {
		dm.ContainProbe = float64(shared) / float64(len(setA))
	}
	if len(setB) > 0 {
		dm.ContainMatch = float64(shared) / float64(len(setB))
	}

	runs := maskRuns(alignRuns(sa, posB, w), probeMask, matchMask)
	dm.GenericChars = maskLen(probeMask)
	dm.Blocks = chainBlocks(runs, probe, match, w)
	dm.BlockCoverProbe = coverage(runs, len(probe.Body), false)
	dm.BlockCoverMatch = coverage(runs, len(match.Body), true)
	ivs := make([]iv, 0, len(runs))
	for _, r := range runs {
		ivs = append(ivs, iv{r.AStart, r.AEnd})
	}
	dm.MatchedChars = unionLen(ivs)
	dm.Relation = classify(dm.BlockCoverProbe, dm.BlockCoverMatch, dm.MatchedChars)
	dm.NumericOnlyInProbe, dm.NumericOnlyInMatch = numericDiff(dm.Blocks, probe, match)
	return dm
}

// alignRuns finds every maximal span of identical folded text.
//
// Seed and extend, grouped by DELTA (the offset difference between the two
// copies). Two shingle positions matching at the same delta and no more than w
// apart prove the text between them identical as well — their windows overlap or
// abut — so a delta's sorted position list collapses into runs by a single walk.
// A separate delta is a separate run, which is exactly right: an insertion in one
// copy shifts everything after it, so "the same page with one word added" comes
// back as two runs, not one broken match.
func alignRuns(sa []uint64, posB map[uint64][]int, w int) []Run {
	byDelta := map[int][]int{} // delta -> probe positions
	for i, h := range sa {
		for _, j := range posB[h] {
			d := j - i
			byDelta[d] = append(byDelta[d], i)
		}
	}
	var runs []Run
	for d, ps := range byDelta {
		sort.Ints(ps)
		start, prev := ps[0], ps[0]
		flush := func() {
			r := Run{AStart: start, AEnd: prev + w, BStart: start + d, BEnd: prev + w + d}
			if r.Len() >= MinRunChars {
				runs = append(runs, r)
			}
		}
		for _, p := range ps[1:] {
			if p-prev <= w {
				prev = p
				continue
			}
			flush()
			start, prev = p, p
		}
		flush()
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].AStart != runs[j].AStart {
			return runs[i].AStart < runs[j].AStart
		}
		return runs[i].BStart < runs[j].BStart
	})
	return runs
}

// coverage is the fraction of one side covered by runs, counting overlapping runs
// once.
//
// The union matters and was got wrong first: summing run lengths reported
// coverage above 1.0 for a document containing a repeated block (a deed attached
// twice to one filing), and a coverage over 1.0 read as an obvious bug in a
// number people were being asked to trust.
func coverage(runs []Run, total int, side bool) float64 {
	if total == 0 {
		return 0
	}
	ivs := make([]iv, 0, len(runs))
	for _, r := range runs {
		if side {
			ivs = append(ivs, iv{r.BStart, r.BEnd})
		} else {
			ivs = append(ivs, iv{r.AStart, r.AEnd})
		}
	}
	covered := unionLen(ivs)
	if covered > total {
		covered = total
	}
	return float64(covered) / float64(total)
}

// maskRuns removes corpus-generic text from every run.
//
// A run is a 1:1 correspondence of equal length, so a mask on the match side maps
// into probe coordinates by subtracting the run's delta; the two masks are then one
// set of intervals in one coordinate system. Masking only the probe side was tried
// first and left half the false positives standing — a footer is generic in both
// documents, but which side the corpus happened to sample it in decided whether it
// was suppressed, so results depended on sampling luck.
//
// Surviving fragments under MinRunChars are dropped: a run cut into
// twenty-character pieces by a mask is not evidence of anything.
func maskRuns(runs []Run, probeMask, matchMask Mask) []Run {
	if len(probeMask) == 0 && len(matchMask) == 0 {
		return runs
	}
	out := make([]Run, 0, len(runs))
	for _, r := range runs {
		delta := r.BStart - r.AStart
		var cut []iv
		cut = appendClipped(cut, probeMask, r.AStart, r.AEnd, 0)
		cut = appendClipped(cut, matchMask, r.BStart, r.BEnd, -delta)
		if len(cut) == 0 {
			out = append(out, r)
			continue
		}
		for _, keep := range subtract(iv{r.AStart, r.AEnd}, cut) {
			if keep.hi-keep.lo < MinRunChars {
				continue
			}
			out = append(out, Run{
				AStart: keep.lo, AEnd: keep.hi,
				BStart: keep.lo + delta, BEnd: keep.hi + delta,
			})
		}
	}
	return out
}

// appendClipped adds the parts of mask that fall inside [lo,hi), shifted by
// `shift` into the target coordinate system.
func appendClipped(dst []iv, mask Mask, lo, hi, shift int) []iv {
	for _, m := range mask {
		if m.hi <= lo || m.lo >= hi {
			continue
		}
		a, b := m.lo, m.hi
		if a < lo {
			a = lo
		}
		if b > hi {
			b = hi
		}
		dst = append(dst, iv{a + shift, b + shift})
	}
	return dst
}

// subtract removes cut intervals from span, returning what survives in order.
func subtract(span iv, cut []iv) []iv {
	sort.Slice(cut, func(i, j int) bool { return cut[i].lo < cut[j].lo })
	var out []iv
	pos := span.lo
	for _, c := range cut {
		if c.hi <= pos {
			continue
		}
		if c.lo > pos {
			hi := c.lo
			if hi > span.hi {
				hi = span.hi
			}
			if hi > pos {
				out = append(out, iv{pos, hi})
			}
		}
		if c.hi > pos {
			pos = c.hi
		}
		if pos >= span.hi {
			return out
		}
	}
	if pos < span.hi {
		out = append(out, iv{pos, span.hi})
	}
	return out
}

func maskLen(m Mask) int { return unionLen([]iv(m)) }

// iv is a half-open interval of folded offsets.
type iv struct{ lo, hi int }

// unionLen is the total length covered by a set of possibly overlapping
// intervals. Counting overlaps twice is the mistake this exists to prevent: it
// produced coverage above 1.0 for a document containing a repeated block (a deed
// attached twice to one filing) and block agreements above 1.0 for any pair whose
// runs met at more than one delta.
func unionLen(ivs []iv) int {
	if len(ivs) == 0 {
		return 0
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].lo < ivs[j].lo })
	total, end := 0, ivs[0].lo
	for _, v := range ivs {
		lo := v.lo
		if lo < end {
			lo = end
		}
		if v.hi > lo {
			total += v.hi - lo
			end = v.hi
		}
	}
	return total
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// chainBlocks groups runs into reportable aligned regions.
//
// A block must mean "this contiguous region of the probe corresponds to this
// contiguous region of the match, in order". That requires monotone advance in
// BOTH documents, and getting it wrong is not a cosmetic problem: a first version
// required only that a run start after the OPEN BLOCK'S START in the match rather
// than after its END, which let runs from unrelated offsets join the same block.
// Two copies of one declaration then reported "probe p1-2 = match p1-4" followed
// by "probe p2-4 = match p1-2" — page ranges that overlap and contradict each
// other, from a pair that is in fact page-for-page identical.
//
// Several blocks stay open at once, and a run joins the one it fits best. With a
// single open block, two interleaved correspondences (the same exhibit attached
// twice, at two offsets) thrashed: runs alternated between them in probe order and
// closed the block on every alternation, so one aligned region came back as
// forty.
func chainBlocks(runs []Run, probe, match FoldedText, w int) []Block {
	var out []Block
	var open []*Block
	// spans keeps each open block's matched probe intervals, so MatchedChars can be
	// a UNION at close time. Summing run lengths double-counts where runs at
	// different deltas overlap, which reported blocks whose matched text exceeded
	// their own extent — an agreement above 1.0, read (correctly) as a bug in a
	// number people were being asked to trust.
	spans := map[*Block][]iv{}

	close := func(b *Block) {
		b.MatchedChars = unionLen(spans[b])
		delete(spans, b)
		out = append(out, *b)
	}
	for _, r := range runs {
		// Retire blocks the remaining runs can no longer reach. Bounds the open
		// list, and is safe because runs arrive in probe order.
		kept := open[:0]
		for _, b := range open {
			if b.aEnd+blockSlack < r.AStart {
				close(b)
				continue
			}
			kept = append(kept, b)
		}
		open = kept

		var best *Block
		bestGap := 0
		for _, b := range open {
			// Overlap of up to w is allowed: two runs at adjacent deltas share
			// their boundary shingle, so requiring strict advance split every
			// OCR-divergent page into one block per difference.
			agap, bgap := r.AStart-b.aEnd, r.BStart-b.bEnd
			if agap < -w || bgap < -w {
				continue
			}
			if agap > blockSlack || bgap > blockSlack {
				continue
			}
			g := abs(agap) + abs(bgap)
			if best == nil || g < bestGap {
				best, bestGap = b, g
			}
		}
		if best == nil {
			b := &Block{aStart: r.AStart, aEnd: r.AEnd, bStart: r.BStart, bEnd: r.BEnd, Runs: 1}
			spans[b] = []iv{{r.AStart, r.AEnd}}
			open = append(open, b)
			continue
		}
		// Gap spans come from the block's ends BEFORE they advance — getting that
		// order wrong reported each disagreement at the offset of the run that
		// followed it, so every gap named the wrong page.
		aTail, bTail := best.aEnd, best.bEnd
		agap, bgap := r.AStart-aTail, r.BStart-bTail
		if r.AEnd > best.aEnd {
			best.aEnd = r.AEnd
		}
		if r.BEnd > best.bEnd {
			best.bEnd = r.BEnd
		}
		best.Runs++
		spans[best] = append(spans[best], iv{r.AStart, r.AEnd})
		// Both sides must have unmatched text for this to be a DISAGREEMENT. A gap
		// on one side only is an insertion, which alignment already expresses as
		// two runs at different deltas, and reporting it here as "these copies
		// differ" beside an empty counterpart read as a bug.
		if agap > 0 && bgap > 0 {
			best.Gaps = append(best.Gaps, Gap{
				ProbePage: probe.PageAt(aTail),
				MatchPage: match.PageAt(bTail),
				ProbeText: rawSpan(probe, aTail, r.AStart),
				MatchText: rawSpan(match, bTail, r.BStart),
			})
		}
	}
	for _, b := range open {
		close(b)
	}
	for i := range out {
		b := &out[i]
		b.SpanChars = b.aEnd - b.aStart
		b.ProbeFromPage, b.ProbeToPage = probe.PageAt(b.aStart), probe.PageAt(b.aEnd-1)
		b.MatchFromPage, b.MatchToPage = match.PageAt(b.bStart), match.PageAt(b.bEnd-1)
		// Filtered and ranked in one place (trimGaps), because ordering before
		// filtering let a long OCR-mangling gap crowd out a five-character change
		// to a recorded distance.
		b.Gaps = trimGaps(b.Gaps)
	}
	// Longest first: the block that proves the relation should lead.
	sort.SliceStable(out, func(i, j int) bool { return out[i].MatchedChars > out[j].MatchedChars })
	return out
}

// minGapChars is the shortest disagreement worth showing on LENGTH alone. Below
// this a gap is usually OCR noise — a dropped comma, an l read as a 1 — and
// reporting every one of them trains people to ignore the list.
const minGapChars = 12

// maxGapsPerBlock caps a block's reported disagreements.
const maxGapsPerBlock = 8

// trimGaps keeps the disagreements worth a person's attention.
//
// A NUMERIC gap is kept whatever its length, and that exemption is the whole
// reason this function is not a one-line length filter. The most consequential
// disagreement two copies of a deed can have is five characters long: "25.00 feet"
// against "30.00 feet", or an auditor file number off by one digit. A plain
// 12-character floor discarded exactly those and kept the paragraph-length OCR
// mangling, which is the opposite of useful — the long gaps are usually a bad read
// and the short numeric ones are usually a different document.
//
// Caught by a test that asserted the changed distance came back in both copies'
// own words. The numbers were already reported in aggregate
// (NumericOnlyInProbe/Match); what was missing was the ability to see WHERE, which
// is what makes a finding checkable against the original.
func trimGaps(gaps []Gap) []Gap {
	kept := make([]Gap, 0, len(gaps))
	for _, g := range gaps {
		if numericallyDifferent(g.ProbeText, g.MatchText) {
			kept = append(kept, g)
			continue
		}
		if len(g.ProbeText) < minGapChars && len(g.MatchText) < minGapChars {
			continue
		}
		kept = append(kept, g)
	}
	// Numeric disagreements first, then longest. Sorted here rather than by the
	// caller so the cap below cannot drop a numeric gap in favour of a long one.
	sort.SliceStable(kept, func(i, j int) bool {
		ni := numericallyDifferent(kept[i].ProbeText, kept[i].MatchText)
		nj := numericallyDifferent(kept[j].ProbeText, kept[j].MatchText)
		if ni != nj {
			return ni
		}
		return len(kept[i].ProbeText)+len(kept[i].MatchText) >
			len(kept[j].ProbeText)+len(kept[j].MatchText)
	})
	if len(kept) > maxGapsPerBlock {
		kept = kept[:maxGapsPerBlock]
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// numericallyDifferent reports whether two spans disagree about any number.
func numericallyDifferent(a, b string) bool {
	sa, sb := map[string]bool{}, map[string]bool{}
	collectNumericRaw(sa, a)
	collectNumericRaw(sb, b)
	if len(sa) == 0 && len(sb) == 0 {
		return false
	}
	for t := range sa {
		if !sb[t] {
			return true
		}
	}
	for t := range sb {
		if !sa[t] {
			return true
		}
	}
	return false
}

// rawSpan turns a folded span back into readable original text, via the fold's
// offset index. Capped: a gap can be an entire re-typed page, and the point is to
// show a person WHAT differs, not to reproduce it.
const maxRawSpan = 240

func rawSpan(f FoldedText, lo, hi int) string {
	if lo < 0 {
		lo = 0
	}
	if hi > len(f.Offs) {
		hi = len(f.Offs)
	}
	if lo >= hi {
		return ""
	}
	a := f.Offs[lo]
	b := len(f.Raw)
	if hi < len(f.Offs) {
		b = f.Offs[hi]
	}
	if a >= b {
		return ""
	}
	s := strings.Join(strings.Fields(f.Raw[a:b]), " ")
	if len(s) > maxRawSpan {
		s = s[:maxRawSpan] + "…"
	}
	return s
}

// numericToken is a number as this corpus writes them: an auditor file number, a
// distance, a bearing component, a dollar amount, a date part. Commas are
// stripped before matching so "1,000" and "1000" are one token; nothing else is
// normalised, because a fold that rounds or reformats digits reports two copies
// that disagree about a distance as agreeing.
var numericToken = regexp.MustCompile(`\d+(?:\.\d+)?`)

var commaInNumber = regexp.MustCompile(`(\d),(\d)`)

// minNumericLen skips single digits: a page number, a list marker or a checkbox
// differs between two copies constantly and means nothing.
const minNumericLen = 2

// numericDiff reports numeric tokens present inside the aligned region on one
// side only.
//
// Restricted to the ALIGNED span deliberately. Diffing whole documents reports
// every number in the 38 pages of a title commitment that the 2-page deed inside
// it does not contain, which is thousands of tokens and no information. Inside a
// block the two texts are asserted to be the same passage, so a number on one
// side and not the other is a real divergence between two copies of one
// instrument.
//
// Multiplicity is ignored — this is a set difference. A page that repeats "25.00"
// three times in one copy and twice in the other is a formatting difference, not
// an altered distance, and counting occurrences reported those constantly.
func numericDiff(blocks []Block, probe, match FoldedText) (onlyProbe, onlyMatch []string) {
	pa := map[string]bool{}
	pb := map[string]bool{}
	for _, b := range blocks {
		collectNumeric(pa, probe, b.aStart, b.aEnd)
		collectNumeric(pb, match, b.bStart, b.bEnd)
	}
	for t := range pa {
		if !pb[t] {
			onlyProbe = append(onlyProbe, t)
		}
	}
	for t := range pb {
		if !pa[t] {
			onlyMatch = append(onlyMatch, t)
		}
	}
	sort.Strings(onlyProbe)
	sort.Strings(onlyMatch)
	return onlyProbe, onlyMatch
}

func collectNumeric(into map[string]bool, f FoldedText, lo, hi int) {
	if lo < 0 {
		lo = 0
	}
	if hi > len(f.Offs) {
		hi = len(f.Offs)
	}
	if lo >= hi {
		return
	}
	a := f.Offs[lo]
	b := len(f.Raw)
	if hi < len(f.Offs) {
		b = f.Offs[hi]
	}
	if a >= b {
		return
	}
	collectNumericRaw(into, f.Raw[a:b])
}

// collectNumericRaw pulls numeric tokens out of already-extracted text.
func collectNumericRaw(into map[string]bool, raw string) {
	// Repeated because a comma every three digits needs more than one pass:
	// "1,234,567" only loses its second comma once the first is gone.
	for {
		next := commaInNumber.ReplaceAllString(raw, "$1$2")
		if next == raw {
			break
		}
		raw = next
	}
	for _, t := range numericToken.FindAllString(raw, -1) {
		if len(t) >= minNumericLen {
			into[t] = true
		}
	}
}

// classify names the relation from block coverage.
//
// Coverage and not the shingle containments, because containment is what shared
// boilerplate inflates: two unrelated county deeds reached ContainProbe 0.34 on
// statutory phrasing alone, which would have been called a containment. Their
// block coverage was 0.06.
// MinClassifyChars is the least matched text a duplicate or containment claim may
// rest on, in folded characters.
//
// A coverage ratio is unusable on a short document, and this corpus is full of
// short documents. 0.83 coverage of a 400-character email is a signature block;
// 0.83 of a two-page deed is the deed. 250 folded characters is roughly a
// substantial paragraph — below it the pair is reported as `overlap`, which says
// "these share text" without asserting one is a copy of the other.
const MinClassifyChars = 250

func classify(coverProbe, coverMatch float64, matched int) Relation {
	if matched < MinClassifyChars {
		return RelOverlap
	}
	p := coverProbe >= DuplicateThreshold
	m := coverMatch >= DuplicateThreshold
	switch {
	case p && m:
		return RelDuplicate
	case p:
		return RelProbeInside
	case m:
		return RelMatchInside
	default:
		return RelOverlap
	}
}
