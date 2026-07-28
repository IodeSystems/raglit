package raglit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Transcription: the document as page-delineated markdown.
//
// Ingest already renders every page and OCRs what needs it. That per-page text
// is thrown away once fragments are built, which is why every consumer that
// needed "what is on page 7" had to redo the work — a project here grew a shell
// script that rasterized and OCR'd page by page purely to materialise what the
// pipeline had already computed and discarded.
//
// So write it out. During ingest it costs nothing: the pages are in hand.
//
// The markdown is the CONTRACT: one `## Page N` heading per page, in order,
// including pages that came back empty. A consumer resolving a hit to a page
// depends on the headings being complete and monotonic — skipping a blank page
// would silently shift every page number after it.
const transcriptionSuffix = ".raglit-transcription.md"

// TranscriptionPath is where a document's transcription lives: beside it, with a
// suffix that says what wrote it, so a re-transcription is detectable and a
// human can tell it from a hand-written note.
func TranscriptionPath(docPath string) string {
	return docPath + transcriptionSuffix
}

// TranscribedPage is one page of a transcription.
type TranscribedPage struct {
	Page    int
	Text    string
	Figures []TranscribedFigure
}

// TranscribedFigure is a described diagram or image on a page. A scanned survey
// is mostly figure, and a transcription that drops it says the page is blank.
type TranscribedFigure struct {
	Kind        string
	Description string
}

// RenderTranscription formats pages as markdown.
//
// Deterministic: same pages in, same bytes out, so a diff between two
// transcriptions is a real change in what was read rather than formatting noise.
func RenderTranscription(docPath string, pages []TranscribedPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Transcription — %s\n\n", filepath.Base(docPath))
	b.WriteString("Page-delineated transcription written by raglit during ingest.\n" +
		"One `## Page N` section per page, in order, including empty pages — a consumer\n" +
		"resolving a match to a page depends on the numbering being complete.\n\n" +
		"This is a TRANSCRIPTION, not an analysis. Cite the source document.\n\n---\n")

	sorted := append([]TranscribedPage(nil), pages...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Page < sorted[j].Page })
	for _, p := range sorted {
		fmt.Fprintf(&b, "\n## Page %d\n\n", p.Page)
		if t := strings.TrimSpace(p.Text); t != "" {
			b.WriteString(t)
			b.WriteString("\n")
		} else {
			b.WriteString("_(no text on this page)_\n")
		}
		for _, f := range p.Figures {
			kind := f.Kind
			if kind == "" {
				kind = "figure"
			}
			fmt.Fprintf(&b, "\n> **%s:** %s\n", kind, strings.TrimSpace(f.Description))
		}
	}
	return b.String()
}

// WriteTranscription materialises the transcription beside the document.
//
// Best-effort by design: a corpus can be read-only, or on a mount that is gone,
// and failing an otherwise-good ingest because a convenience file could not be
// written would be the wrong trade. The caller logs and continues.
func WriteTranscription(docPath string, pages []TranscribedPage) (string, error) {
	out := TranscriptionPath(docPath)
	if err := os.WriteFile(out, []byte(RenderTranscription(docPath, pages)), 0o644); err != nil {
		return "", err
	}
	return out, nil
}
