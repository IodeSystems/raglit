package raglit

import (
	"regexp"
	"strings"
	"unicode"
)

// Shingling — the comparable form of a document, and the primitive both
// near-duplicate detection and containment detection are built on.
//
// WHY NOT EMBEDDINGS. The question "is this upload a copy of something we
// already hold?" looks like a similarity question and is not one. Cosine over a
// whole document fails the two cases that matter here in opposite directions:
//
//   - A 2-page deed appended as an exhibit to a 40-page title commitment is 5%
//     of that document's text. Its contribution to the commitment's mean-pooled
//     vector is ~5%, so the pair scores LOW while the deed is in fact wholly
//     present. Every appended exhibit is missed.
//   - Two UNRELATED recorded deeds from the same county share their statutory
//     form, their notary block, their legal-description boilerplate and their
//     auditor's certificate. That is most of the tokens on a one-page deed, so
//     the pair scores HIGH while sharing no operative content. Every pair of
//     county records is a false positive.
//
// Both failures are structural, not a threshold that needs tuning. Embeddings
// answer "are these about the same thing"; the question here is "does this text
// literally occur there", which is a set/sequence question. Shingles answer it
// exactly, need no model, and therefore run offline and in CI. Embeddings are
// left for what they are good at (the image case, and topical retrieval).
//
// A shingle is a fixed-width window of the FOLDED text taken at every position.
// Stride is 1 and must be: the same passage appearing at a different offset in
// another document has to produce the same shingles, and any stride > 1 pins the
// window grid to the offset, so an exhibit reproduced 300 characters further
// into a filing would share nothing. This is also why raglit's OverlapFragments
// windows cannot be reused for this — they are position-derived by design (they
// exist so a fragment reassembles to an exact source span), which is the one
// property a shingle must not have.

// FoldWidth is the shingle width, in folded bytes.
//
// Measured on the ardley-v-brannock corpus — 233 indexed documents, 2.49 MB of
// fragment text, 441 true pages, 1.59 MB folded — by re-sketching the whole index
// at w = 16/24/32/48/64 and running the all-pairs audit at each:
//
//	w    reported pairs   duplicate-or-containment   hardest real pair
//	16        292                  56                      0.40
//	24        316                  58                      0.49
//	32        399                  57                      0.59
//	48        281                  65                      0.55
//	64        364                  60                      0.44
//
// Every width finds every known duplicate and every known containment, and the
// known false positive is suppressed at every width. So w does NOT decide the
// boilerplate problem — generic-text masking does (see Mask in similar.go). What w
// decides is how much of a DEGRADED copy is recovered, and the last column is that:
// the 2008 record of survey against the copy reproduced inside the 2008 lot
// certification, whose OCR is substantially garbled. Coverage peaks at w=32 and
// falls away in both directions.
//
// It falls off above 32 for the obvious reason — one wrong character destroys w
// shingles, so a wide window is fragile to a bad read. It falls off BELOW 32 for a
// less obvious one: a short shingle is far more likely to occur across many
// unrelated documents, so it clears the generic-DF cutoff and real text gets masked
// away. At w=16 even the byte-identical Graham pair drops to 0.85 coverage, against
// 1.00 at w=48. Both failure modes are worst exactly on the pages that are hardest
// to read, which is where the tool has to work.
const FoldWidth = 32

// SampleMod is the sampling divisor for the stored sketch: a shingle is kept
// when hash % SampleMod == 0.
//
// This is NOT a MinHash signature, and the difference is the load-bearing part.
// A fixed-K MinHash signature estimates Jaccard well and CONTAINMENT not at all:
// the fraction of agreeing signature positions between a 2-page deed and the
// 40-page commitment containing it estimates |A∩B|/|A∪B| ≈ 0.05, and there is no
// way to recover |A∩B|/|A| = 1.0 from it, because the signature has thrown away
// the sizes. A first pass at this used K=128 MinHash and reported the deed-inside-
// commitment case as a 0.05 similarity with no containment signal — the exact
// failure the whole feature exists to avoid.
//
// A mod-p sample (FracMinHash) is CONSISTENT: whether a shingle is sampled
// depends only on the shingle, never on the set it came from, so
// sample(A) ∩ sample(B) = sample(A ∩ B) and |A∩B| is estimable directly. Sizes
// are kept separately (shingle_pages.total), so both measures fall out.
//
// 32 keeps 1 shingle in 32. Measured on this corpus: 441 true pages, 1,587,596
// folded characters, 1,290,196 distinct shingles, of which 39,778 are sampled —
// 1 in 32.4, so the sampling is doing what it says. That is ~90 samples on an
// average page (3,600 folded chars). The smallest region worth reporting is a
// paragraph, ~250 folded characters, which contributes ~8 samples at full
// containment — comfortably above the 3-sample floor candidate generation uses.
//
// The whole index is 39,778 rows, against 1.29 M rows if the shingles were stored
// whole: a 32x saving on a table that has to be scanned per probe, and candidate
// generation loses nothing by it, because every candidate pair is then rescored
// EXACTLY over all shingles (see Compare in similar.go).
const SampleMod = 32

// FoldedText is text reduced to its comparable form, with the index needed to
// map a finding back to something a person can read.
type FoldedText struct {
	// Raw is the text the fold was taken of — pages joined by PageJoin. Offs
	// indexes into this.
	Raw string
	// Body is the fold: lowercase letters and digits only, no separators.
	Body string
	// Offs[i] is the byte offset in Raw of the i'th byte of Body.
	Offs []int
	// Pages are page boundaries in FOLDED (Body) offsets, in page order.
	Pages []FoldedPage
}

// FoldedPage locates one page inside a FoldedText as a half-open [Start,End)
// span of folded offsets. A page whose text folds to nothing (a blank scan, or a
// page that is pure figure) is still listed, with Start == End: page numbering
// has to stay complete or every alignment after it reports the wrong page.
type FoldedPage struct {
	Page  int
	Start int
	End   int
}

// PageJoin separates pages in FoldedText.Raw. It folds away entirely, so it can
// never create or destroy a shingle — the fold is what makes the choice of
// separator irrelevant.
const PageJoin = "\n\n"

// PageAt reports which page a folded offset lies on, and 0 when it lies past the
// end. Binary search would be faster; page counts here are in the tens.
func (f FoldedText) PageAt(off int) int {
	for _, p := range f.Pages {
		if off >= p.Start && off < p.End {
			return p.Page
		}
	}
	// An offset exactly at the end of the last non-empty page belongs to it.
	for i := len(f.Pages) - 1; i >= 0; i-- {
		if f.Pages[i].End > f.Pages[i].Start && off <= f.Pages[i].End {
			return f.Pages[i].Page
		}
	}
	return 0
}

// pleadingLineNo is a line number printed down a pleading's left margin.
// Deliberately identical to kgraph's `lineNo` (quotes.go): the two tools must
// agree about what a document's text IS, or kgraph reports a quote present in a
// document raglit says holds no copy of it.
var pleadingLineNo = regexp.MustCompile(`(?m)^[ \t]{0,4}\d{1,2}[ \t](\S)`)

// stripPleadingLineNumbers removes a pleading's margin line numbers, but only
// from text that actually numbers its lines.
//
// Ported from kgraph's quotes.go. The threat is real in principle: a declaration
// numbers lines 1-28 down every page while the deed photocopied in as its Exhibit B
// does not, so an interleaved margin number every ~60 characters would destroy 32
// of every 60 shingles in the copy and not in the original — turning a wholly
// contained exhibit into an apparent non-match.
//
// MEASURED ON THIS CORPUS IT IS A NO-OP, and the reason is worth recording because
// it is not what the kgraph comment leads you to expect. raglit's vision
// transcription does not interleave the numbers: it emits them as a BLOCK at the
// top of the page —
//
//	1
//	2
//	…
//	32
//
//	IN THE SUPERIOR COURT OF THE STATE OF WASHINGTON
//
// — so each number is alone on its line, and the regex (which requires a non-space
// character after the number, deliberately) matches none of them. Swept with the
// strip on and off over the pleading pairs: identical scores to the character.
//
// So the block form is what actually costs, and it is handled elsewhere: folded, it
// becomes one ~55-character run identical on every pleading page in the corpus,
// which is above MinRunChars and would be spurious agreement — and generic-text
// masking removes it, because it appears in far more than GenericDocFreq documents.
//
// Kept regardless. It costs one pass over the text, it is what keeps this fold
// byte-compatible with the one kgraph verifies quotes against (a divergence there
// is a quote silently failing to match), and a pdftotext extraction of the same
// filing DOES interleave the numbers — the no-op is a property of this corpus's
// OCR engine, not of the format.
//
// kgraph's caveat applies unchanged and is why this is conditional rather than
// unconditional: a deed wraps as "...lines of Lot\n2 of said plat", and stripping
// that "2" deletes real content. The gate (at least 8 numbered lines, and at least
// a fifth of all lines numbered) is what tells a numbered pleading from a wrapped
// sentence.
//
// Applied PER PAGE rather than per document, the one deliberate divergence from
// kgraph. kgraph folds a whole transcription at once, so a 40-page filing whose 30
// pleading pages are numbered and whose 10 exhibit pages are not passes the gate as
// a whole and gets the strip applied to the exhibits too. Per page, each page is
// judged on its own evidence and an exhibit page is left alone.
func stripPleadingLineNumbers(s string) string {
	var lines, numbered int
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lines++
		if pleadingLineNo.MatchString(ln + " ") {
			numbered++
		}
	}
	if lines == 0 || numbered < 8 || numbered*5 < lines {
		return s
	}
	return pleadingLineNo.ReplaceAllString(s, " $1")
}

// FoldPages reduces paged text to its comparable form.
//
// Kept byte-for-byte compatible with kgraph's foldTextIdx: lowercase, keep
// unicode letters and digits, drop everything else, and record the source offset
// of every byte written. Digits are kept STRICTLY — no date or number
// canonicalisation of any kind. In this corpus the highest-value tokens on a page
// are exactly the ones a normaliser would eat: an auditor file number
// (AF#200808180120), a bearing (N 89°14'32" W), a distance (25.00 feet). Two
// copies of one deed that disagree about a distance are a serious finding, and
// any fold that rounds, reformats or strips digits reports them as agreeing.
//
// The separators between those digits DO fold away — "AF#200808180120",
// "AF 200808180120" and "AF# 200808180120" all become "af200808180120" — which
// is the same reason kgraph's normalize() drops punctuation from an identifier.
func FoldPages(pages []PageText) FoldedText {
	var raw strings.Builder
	var body strings.Builder
	offs := make([]int, 0, 1024)
	var fps []FoldedPage
	for i, p := range pages {
		if i > 0 {
			raw.WriteString(PageJoin)
		}
		base := raw.Len()
		t := stripPleadingLineNumbers(p.Text)
		raw.WriteString(t)
		start := body.Len()
		for j, r := range t {
			l := unicode.ToLower(r)
			if !unicode.IsLetter(l) && !unicode.IsDigit(l) {
				continue
			}
			// ToLower can change a rune's encoded width, and a multi-byte rune
			// folds to one Offs entry per byte written, so the mapping stays
			// aligned. Same reasoning as kgraph's foldTextIdx.
			n := body.Len()
			body.WriteRune(l)
			for range body.Len() - n {
				offs = append(offs, base+j)
			}
		}
		fps = append(fps, FoldedPage{Page: p.Page, Start: start, End: body.Len()})
	}
	return FoldedText{Raw: raw.String(), Body: body.String(), Offs: offs, Pages: fps}
}

// Shingles hashes every width-w window of folded text, at stride 1.
//
// Result index i is the shingle STARTING at folded offset i, and callers depend
// on that: the alignment in similar.go recovers matched spans by chaining
// positions, so a position must mean an offset and nothing else. Fewer than w
// bytes yields nothing — a page too short to shingle contributes no evidence,
// which is correct, but it is also why a page's folded length is stored
// alongside its shingle count.
func Shingles(folded string, w int) []uint64 {
	if w <= 0 || len(folded) < w {
		return nil
	}
	out := make([]uint64, len(folded)-w+1)
	for i := range out {
		out[i] = hashShingle(folded[i : i+w])
	}
	return out
}

// hashShingle is FNV-1a with a splitmix64 finalizer.
//
// The finalizer earns less than expected and is kept anyway, which is worth saying
// plainly because the reason usually given for it is wrong here. Sampling keeps a
// shingle when hash % SampleMod == 0 — the low 5 bits at mod 32 — and FNV-1a's low
// bits are widely described as weakly mixed. Measured over 1,828,509 shingles of
// this corpus, they are not: raw FNV-1a mod 32 keeps 1 shingle in 33.2 against a
// target of 32, and with the finalizer 1 in 32.0. A 4% bias, not the order of
// magnitude the folklore predicts. FNV-1a's final multiply by an odd prime makes
// every low bit depend on the whole input, which is enough at this modulus.
//
// Kept because the modulus is a knob (SimilarOpts.Mod), the failure it guards
// against is silent and distributional rather than an error, and three multiplies
// per shingle is unmeasurable next to the string compare. Removing it would save
// nothing worth the argument.
func hashShingle(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h
}

// Sampled reports whether a shingle hash is in the stored sketch.
func Sampled(h uint64, mod int) bool { return mod <= 1 || h%uint64(mod) == 0 }

// PageSketch is one page's stored evidence: the sampled shingle hashes, plus the
// sizes that sampling throws away.
type PageSketch struct {
	Page int
	// Chars is the page's folded length. Kept because Total alone cannot tell a
	// blank page (no shingles, nothing to say) from a page shorter than one
	// shingle (no shingles, but it does hold text).
	Chars int
	// Total is the count of DISTINCT shingles on the page — the denominator of
	// containment. Sampling makes the numerator estimable and would make this
	// unrecoverable, so it is stored.
	Total int
	// Hashes are the sampled shingles, distinct and sorted.
	Hashes []uint64
}

// SketchPages builds the stored sketch for a folded document, one entry per page.
// Pages are emitted even when they hold no shingles, so a re-sketch replaces a
// document's rows completely and page numbering stays contiguous.
func SketchPages(f FoldedText, w, mod int) []PageSketch {
	out := make([]PageSketch, 0, len(f.Pages))
	for _, p := range f.Pages {
		ps := PageSketch{Page: p.Page, Chars: p.End - p.Start}
		seen := map[uint64]bool{}
		for _, h := range Shingles(f.Body[p.Start:p.End], w) {
			if seen[h] {
				continue
			}
			seen[h] = true
			ps.Total++
			if Sampled(h, mod) {
				ps.Hashes = append(ps.Hashes, h)
			}
		}
		sortU64(ps.Hashes)
		out = append(out, ps)
	}
	return out
}

// sortU64 sorts in place. A hand-rolled insertion/merge is not worth it; this is
// sort.Slice's job and the slices are short (tens of entries per page).
func sortU64(v []uint64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
