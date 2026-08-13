package raglit

import (
	"html"
	"regexp"
	"strings"
)

// What the INDEX holds, as distinct from what the transcription artifact holds.
//
// A layout-aware VLM returns markup: chandra emits
//
//	<div data-bbox="596 138 728 156" data-label="Page-Header"><p>VOL 215 PAGE 159</p></div>
//
// and that is genuinely useful in the stored transcription — the boxes are how a
// region is located and how a person checks a quotation against the pixels. It
// is worth nothing to a searcher, and it is not free:
//
//	measured on the delano corpus 2026-08-10
//	  413 of 2692 fragments carried this markup (15%)
//	  40% of their bytes were tags — ~1.3 MB indexed, embedded and searched
//	  one 1947 deed was 49% tags
//
// It costs three ways. Fragment budget is spent on attributes. Embeddings encode
// `data-bbox` coordinates as if they were words. And the SEGMENTER is handed
// twice the text it needs, which is how a 2-page deed came back
// `arguments are not a JSON object: unexpected end of JSON input` — the reply
// truncated mid-object. Measured: with chandra the segmenter produced no valid
// reply at all; with Qwen it produced valid JSON that dropped 1184 of 1234
// characters. Neither model was the fault.

var (
	// blockTag ends a line of text. A <br/> or a </div> is a place the page had a
	// break, so it becomes one.
	blockTag = regexp.MustCompile(`(?i)</?(br|div|p|tr|li|h[1-6]|table|thead|tbody|blockquote|section|article)\b[^>]*>`)
	// cellTag separates values on one line — a row should stay a row.
	cellTag = regexp.MustCompile(`(?i)</?(td|th)\b[^>]*>`)
	// imgTag carries an alt description, which IS content: the pipeline asks the
	// model to describe figures inline, and dropping the alt would throw away the
	// only text a photograph or a barcode ever produces.
	imgTag = regexp.MustCompile(`(?i)<img\b[^>]*\balt\s*=\s*"([^"]*)"[^>]*>`)
	// anyTag is everything left over: <b>, <u>, <sup>, <span>, stray closers.
	// These are INLINE and must vanish without a trace — replacing them with a
	// space inserts whitespace the page never had, which turns
	// `Afro-Shirazi<sup>1</sup>,` into `Afro-Shirazi ,` and breaks an exact-phrase
	// search on text that was read perfectly. That mistake cost ~4 points of
	// apparent model error on olmOCR-bench before it was found in the scorer;
	// this is the same rule applied where it actually belongs.
	// The `[a-zA-Z!/]` is load-bearing: a bare `<` in prose is NOT a tag opener,
	// and `<[^>]*>` happily matched `< 2 acres and the setback is >` and deleted
	// the sentence between them. Legal and survey text is full of bare
	// comparisons; a stripper that eats them is worse than no stripper.
	anyTag = regexp.MustCompile(`</?[a-zA-Z!][^>]*>`)
	// manySpaces collapses runs left behind by removed markup, per line, so the
	// line structure the block tags established survives.
	manySpaces = regexp.MustCompile(`[ \t]+`)
	// blankRuns collapses three-or-more newlines to a paragraph break.
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

// StripLayoutMarkup removes a VLM's layout markup, keeping the text and the
// figure descriptions.
//
// Block tags become line breaks and inline tags become nothing — the distinction
// is the whole point, and getting it backwards is worse than not stripping at
// all, because it silently corrupts phrases rather than obviously leaving tags.
func StripLayoutMarkup(s string) string {
	if !strings.Contains(s, "<") {
		return s // the overwhelmingly common case: nothing to do, no allocation
	}
	s = imgTag.ReplaceAllString(s, "$1\n")
	s = cellTag.ReplaceAllString(s, " ")
	s = blockTag.ReplaceAllString(s, "\n")
	s = anyTag.ReplaceAllString(s, "")
	// Entities come back as characters: a searcher types & and " , not &amp; and
	// &quot;, and an embedding of the entity name is an embedding of nothing.
	s = html.UnescapeString(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(manySpaces.ReplaceAllString(line, " "), " "))
	}
	return strings.Trim(blankRuns.ReplaceAllString(b.String(), "\n\n"), "\n")
}

// FlattenForIndex is what the index should hold for a page: layout markup
// removed, markdown tables reduced to plain lines.
//
// The two halves were written weeks apart for the same reason and neither was
// wired to anything. Composed here so there is ONE answer to "what goes in the
// index", and one place to change it.
func FlattenForIndex(s string) string {
	return FlattenMarkdownForIndex(StripLayoutMarkup(s))
}

// Text a model DESCRIBED, as distinct from text it transcribed.
//
// A layout-aware VLM handed a photograph does not fail — it writes down what it
// saw. On a photo of a car in a driveway chandra returns 700 characters naming
// the make, the model and the licence plate. That is genuinely useful and it is
// how a photograph becomes searchable at all.
//
// It is also NOT what the record says. Nobody wrote "a red Chevrolet Malibu";
// the model inferred it, and it can be wrong about every part of it. Indexed
// with no mark, that sentence is a fragment exactly like a sentence transcribed
// off a fax — same shape in search results, same appearance of being quotable.
// In a corpus that may be cited in a filing, the difference is the whole point,
// and the codebase already draws it for generated CAPTIONS (identity.go writes
// origin='identity': findable by it, not quotable from it). This applies the
// same rule to the other thing a model makes up.
//
// The signal comes from the model, not from a guess about the file: chandra
// wraps what it describes in a region labelled Image and puts the description in
// an <img alt>. A scanned page whose text it read is labelled Text, Table,
// Page-Header and so on.
var (
	imageRegion = regexp.MustCompile(`(?is)<(div|figure)\b[^>]*\bdata-label\s*=\s*"(image|figure|picture|photo)"[^>]*>(.*?)</(?:div|figure)>`)
	anyImgTag   = regexp.MustCompile(`(?i)<img\b[^>]*\balt\s*=\s*"([^"]*)"[^>]*>`)
)

// DescribedFraction is how much of a page's INDEXABLE text came from the model
// describing an image, between 0 and 1. A page with no such regions is 0; a
// photograph is ~1.
func DescribedFraction(raw string) float64 {
	total := len(FlattenForIndex(raw))
	if total == 0 {
		return 0
	}
	described := 0
	// Region text first — it contains the <img alt> as well as any prose the
	// model wrote around it, and both are description.
	rest := raw
	for _, m := range imageRegion.FindAllStringSubmatch(raw, -1) {
		described += len(FlattenForIndex(m[3]))
		rest = strings.Replace(rest, m[0], "", 1)
	}
	// Then any alt text OUTSIDE such a region: a figure inline in a real page,
	// where the surrounding transcription is genuine.
	for _, m := range anyImgTag.FindAllStringSubmatch(rest, -1) {
		described += len(strings.TrimSpace(m[1]))
	}
	if described > total {
		described = total
	}
	return float64(described) / float64(total)
}

// FragOriginDescribed marks a fragment whose text is a model's account of an
// IMAGE rather than a transcription of writing.
//
// It sits beside origin='identity' (identity.go), which marks a generated
// caption, and means the same thing in the same column: findable by it, not
// quotable from it. A corpus that may be cited needs the difference between
// "the fax says" and "the model thinks the car is a Chevrolet" to survive into
// the search result, and until this existed it did not.
const FragOriginDescribed = "described"

// A note for anything that filters fragments by origin.
//
// There are two kinds of generated text in this column and they are not
// interchangeable. 'identity' is a caption ABOUT a document — excluded from the
// document's own words, because a re-run must read the document and not the last
// caption of it. 'described' IS a document's content: a photograph has no other
// text, and dropping it means the photograph has none.
//
// So the predicate for "the document's own indexed words" is
// `origin <> 'identity'`, never `origin = ''`. Six queries were written the
// second way before descriptions existed, and each one silently changed meaning
// the moment they did: the document list reported photographs as empty,
// get_document returned nothing for them, and the cross-index pool would have
// re-imported them with their only content stripped.

// describedPageThreshold is how much of a page must be description before the
// page is called one.
//
// High, and deliberately. Marking is a claim that a reader should not quote this
// as the record, so a page that is mostly transcription with a figure in it must
// NOT be marked — the transcription is real and calling it generated would be
// its own kind of lie. This catches the case that matters and is honest about
// leaving the mixed one alone: see FragOriginDescribed.
const describedPageThreshold = 0.9

// IsDescribedPage reports whether a page is essentially a model's description of
// an image rather than a transcription of text.
func IsDescribedPage(raw string) bool {
	return DescribedFraction(raw) >= describedPageThreshold
}
