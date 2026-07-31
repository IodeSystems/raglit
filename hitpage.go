package raglit

import "strings"

// Which page a hit is actually on.
//
// A fragment is not a page. The text fragmenter windows across a whole document
// — roughly 8,000 characters with overlap — so one fragment routinely covers
// three pages of a PDF or three messages of an email archive. `fragments.page`
// records where the fragment STARTED, and search reported that verbatim, so a
// quotation from the fourth message of an archive was cited as the second.
//
// The correct answer was already stored. `fragments.page_spans` carries the
// offset of every page boundary inside the fragment; nothing read it. This is
// the reader.
//
// It is an offset lookup and not a guess: find where the query's terms actually
// occur in the fragment, then ask the spans which page owns that offset. Where
// no term is found — a vector hit has no literal terms to locate — it falls back
// to the fragment's start page, which is the old behaviour and is the best
// available answer rather than a wrong one dressed up.

// HitPage resolves the page a match sits on, given the fragment's recorded page
// spans and the query that found it.
//
// startPage is returned unchanged when the fragment covers one page, when the
// spans are missing, or when no query term can be located — all three mean
// "nothing better is knowable here".
func HitPage(startPage int, spansJSON, text, query string) int {
	spans := DecodePageSpans(spansJSON)
	if len(spans) < 2 {
		// One span (or none) means the fragment does not cross a page boundary,
		// so the start page is already right.
		return startPage
	}
	off := firstTermOffset(text, query)
	if off < 0 {
		return startPage
	}
	page := startPage
	for _, sp := range spans {
		if sp.Off > off {
			break
		}
		page = sp.Page
	}
	return page
}

// firstTermOffset finds the earliest position in text where any term of the
// query occurs, case-insensitively. -1 when none does.
//
// EARLIEST rather than best: a fragment matched because of some term, and the
// first one present is the most defensible anchor available without asking FTS5
// for per-term offsets. Reporting the page of the first hit term beats reporting
// the page the window happened to open on by the whole width of the window.
func firstTermOffset(text, query string) int {
	lower := strings.ToLower(text)
	best := -1
	for _, term := range queryTerms(query) {
		if i := strings.Index(lower, term); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// queryTerms strips FTS5 syntax down to the words a reader would recognise.
//
// Operators, quotes and column filters are not text to look for: searching a
// fragment for the literal string "AND" would anchor a citation on a stopword
// and put the quotation on whatever page that fell on.
func queryTerms(query string) []string {
	repl := strings.NewReplacer(
		`"`, " ", `(`, " ", `)`, " ", `*`, " ", `:`, " ", `^`, " ", `-`, " ", `+`, " ",
	)
	var out []string
	for _, f := range strings.Fields(strings.ToLower(repl.Replace(query))) {
		switch f {
		case "and", "or", "not", "near":
			continue
		}
		if len(f) < 2 {
			continue
		}
		out = append(out, f)
	}
	return out
}
