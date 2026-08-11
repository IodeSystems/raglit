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
