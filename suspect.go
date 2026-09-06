package raglit

import (
	"fmt"
	"strings"
	"unicode"
)

// Is this transcription plausibly the page?
//
// raglit already fails loudly when a generation LOOPS or when a loop-break retry
// comes back thinner than the cut pass. Both of those are model faults it can
// see. This is the one it could not: a read that succeeds, returns clean
// well-formed text, and is simply not the page.
//
// The case that made it necessary. A six-page summary-judgment order — the
// operative order in a live matter — transcribed to 1,148 bytes containing
// nothing but the diagonal "UNOFFICIAL DOCUMENT" watermark, read letter by
// letter down the page:
//
//	## Page 1
//	T
//	   EN
//	  M
//	 CU
//	DO
//
// No loop. No error. The job recorded `done`, the document entered the index,
// and every quotation anyone later took from that order would fail to verify
// against a transcription that had never contained the order at all. Across the
// corpus the same shape hit a record of survey, a vesting deed and a recorded
// agreement.
//
// So: FLAG, never fail. A genuinely near-empty page is ordinary — a divider, a
// signature page, the back of a form — and refusing to index one would stop
// ingest on documents that are fine. What was missing is not a gate, it is a
// mark on the page saying "a human should look at this", carried into the
// transcription where the next reader and `kg` will both meet it.

// suspicion thresholds.
//
// Deliberately loose. A false flag costs someone ten seconds; a missed one puts
// a watermark in the record as though it were a court order.
const (
	// A page whose lines average fewer than this many characters is text read
	// down the page rather than across it — the signature of a rotated
	// watermark, a spine, or a stamp read as content.
	suspectMeanLine = 6.0
	// Below this many characters a page is either nearly blank or was not read.
	// Either way it is worth a human's glance before anything is quoted from it.
	suspectMinChars = 220
	// A page needs at least this many lines before the mean is meaningful; two
	// short lines are not a pattern.
	suspectMinLines = 4
)

// Suspicion returns a human-readable reason this page looks wrong, or "".
//
// The reason is written for the person who has to act on it: what was seen, and
// what it usually means.
func Suspicion(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return "" // an empty page is honestly empty; the renderer already shows it
	}

	var lines []string
	for _, ln := range strings.Split(t, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		return ""
	}

	letters := 0
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		}
	}

	total := 0
	for _, ln := range lines {
		total += len([]rune(ln))
	}
	mean := float64(total) / float64(len(lines))

	switch {
	case len(lines) >= suspectMinLines && mean < suspectMeanLine:
		return fmt.Sprintf(
			"lines average %.1f characters over %d lines — this is the signature of text read DOWN the page "+
				"(a diagonal watermark, a stamp, a spine) rather than the page's own text. Read the original before quoting it.",
			mean, len(lines))
	case letters < suspectMinChars:
		return fmt.Sprintf(
			"only %d letters and digits on the whole page — either it is nearly blank, or the read failed and produced "+
				"something that is not the page. Read the original before quoting it.",
			letters)
	}
	return ""
}

// SuspectPages returns the page numbers a human should look at, with reasons.
func SuspectPages(pages []TranscribedPage) map[int]string {
	out := map[int]string{}
	for _, p := range pages {
		if why := Suspicion(p.Text); why != "" {
			// A page carrying a described figure is not suspect on length alone:
			// a survey sheet is mostly drawing, and the description IS the read.
			if len(p.Figures) > 0 {
				continue
			}
			out[p.Page] = why
		}
	}
	return out
}
