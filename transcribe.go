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

// IsTranscription reports whether a path is raglit's own transcription output.
func IsTranscription(path string) bool {
	return strings.HasSuffix(path, transcriptionSuffix)
}

// TranscribedPage is one page of a transcription.
type TranscribedPage struct {
	Page    int
	Text    string
	Figures []TranscribedFigure
	// Corrected is a person's replacement for this page, when one exists. Set by
	// RenderTranscriptionCorrected; never read off disk.
	Corrected PageCorrection
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
// RenderTranscriptionCorrected renders with a person's page corrections applied.
//
// The corrections are NOT stored in the file this produces. That file is
// regenerated on every read, and corrections kept in it were destroyed twice by
// ordinary re-reads before they were moved into the judgement store. Rendering
// applies them; the store keeps them.
func RenderTranscriptionCorrected(docPath string, pages []TranscribedPage, corrections map[int]PageCorrection) string {
	if len(corrections) == 0 {
		return RenderTranscription(docPath, pages)
	}
	out := make([]TranscribedPage, 0, len(pages))
	for _, p := range pages {
		if c, ok := corrections[p.Page]; ok {
			p.Text = c.Text
			p.Corrected = c
		}
		out = append(out, p)
	}
	return RenderTranscription(docPath, out)
}

func RenderTranscription(docPath string, pages []TranscribedPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Transcription — %s\n\n", filepath.Base(docPath))
	b.WriteString("GENERATED FILE — raglit rewrites this on every read. Edits made here are\n" +
		"lost, silently, the next time the document is ingested or re-read.\n\n" +
		"It is an EXPORT, for tools that do not link raglit and do not need to. The\n" +
		"text lives in raglit's index; this is a copy of it on disk.\n\n" +
		"To correct a page so the correction SURVIVES and is re-issued into every\n" +
		"later render:\n\n" +
		"    raglit transcribe --correct --page N <document> < corrected-text.txt\n\n" +
		"One `## Page N` section per page, in order, including empty pages — a consumer\n" +
		"resolving a match to a page depends on the numbering being complete.\n\n" +
		"This is a TRANSCRIPTION, not an analysis. Cite the source document.\n")

	sorted := append([]TranscribedPage(nil), pages...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Page < sorted[j].Page })

	// A read can succeed and still not be the page. Where it looks wrong, say so
	// AT THE TOP as well as on the page — a reader who opens this file to check a
	// quotation must not have to scroll to discover that page 3 is a watermark.
	if sus := SuspectPages(sorted); len(sus) > 0 {
		nums := make([]int, 0, len(sus))
		for n := range sus {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		b.WriteString("\n> ⚠ **This transcription may be incomplete.** ")
		fmt.Fprintf(&b, "%d of %d page(s) do not look like a page of text: ", len(nums), len(sorted))
		strs := make([]string, len(nums))
		for i, n := range nums {
			strs[i] = fmt.Sprintf("%d", n)
		}
		b.WriteString(strings.Join(strs, ", "))
		b.WriteString(". See the note under each. Read the original before quoting from them.\n")
	}
	b.WriteString("\n---\n")

	for _, p := range sorted {
		// A corrected page says so, and says how the correction was established.
		// A reader who cannot tell a machine read from a checked one has to treat
		// both as unverified, which wastes the checking.
		if c := p.Corrected; c.Text != "" {
			fmt.Fprintf(&b, "\n> ✔ **Page %d was corrected by hand", p.Page)
			if c.By != "" {
				fmt.Fprintf(&b, " (%s", c.By)
				if c.At != "" {
					fmt.Fprintf(&b, ", %s", c.At)
				}
				b.WriteString(")")
			}
			b.WriteString(".**")
			if c.Note != "" {
				fmt.Fprintf(&b, " %s", c.Note)
			}
			b.WriteString("\n")
		}
		// The per-page warning goes ABOVE the heading, never under it.
		//
		// Everything after "## Page N\n\n" is the page's text verbatim, and that
		// is load-bearing: it is what lets a consumer turn an offset found in
		// this markdown into an offset into Text, which is how a quotation is
		// attributed back to the region — and to the crop — it was read from.
		// A banner inserted under the heading shifts every one of those offsets
		// by its own length, silently, and only on the pages already flagged as
		// suspect. The reader still meets the warning before the page.
		if why := Suspicion(p.Text); why != "" && len(p.Figures) == 0 {
			fmt.Fprintf(&b, "\n> ⚠ **Check this page against the original.** %s\n", why)
		}
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
	return WriteTranscriptionCorrected(docPath, pages, nil)
}

// WriteTranscriptionCorrected writes the export with a person's corrections
// applied. See RenderTranscriptionCorrected for why they are applied here rather
// than stored here.
func WriteTranscriptionCorrected(docPath string, pages []TranscribedPage, corrections map[int]PageCorrection) (string, error) {
	// Never transcribe a transcription. The ignore list keeps these out of
	// discovery, but an explicit `raglit index <file>` bypasses discovery, and one
	// slip would start writing x.md.raglit-transcription.md.raglit-transcription.md.
	if IsTranscription(docPath) {
		return "", fmt.Errorf("raglit: %s is a transcription; refusing to transcribe it",
			filepath.Base(docPath))
	}
	out := TranscriptionPath(docPath)
	if err := os.WriteFile(out, []byte(RenderTranscriptionCorrected(docPath, pages, corrections)), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// writebackForDoc decides the transcription writeback for ONE document.
//
// The daemon runs from its own home (~/.raglit) and opens indexes by a
// namespaced name; it never sees a project's own .raglit/config.json, because
// home discovery picks the project home OR the default home and never overlays
// them. That left a per-project setting only expressible in the daemon's global
// config under a namespaced key — action at a distance, and nobody would find it.
//
// Whether to write a file NEXT TO a document is a property of that document's
// project, and the document's own path can find it. So: walk up from the
// document for a project config and let it decide; fall back to the store's
// setting when there is none. A project can turn it on for itself without
// touching the daemon, and off again the same way.
//
// projectFlagForDoc (emailattach.go) is that walk; the attachment sidecar is
// decided the same way, for the same reason.
// correctionsForDoc loads a document's hand corrections from the project that
// owns it, so an INGEST re-issues them the same way `raglit transcribe` does.
//
// Without this the two paths disagree: a person corrects a page, and the next
// ingest silently writes an export without the correction — which is the exact
// loss the correction store was built to end, arriving through the other door.
//
// Best-effort. A document under no project, an unreadable store, a corpus on a
// read-only mount: all render uncorrected rather than failing an ingest, because
// a transcription without corrections is still a transcription.
func correctionsForDoc(docPath string) map[int]PageCorrection {
	dir := ProjectDirForDoc(docPath)
	if dir == "" {
		return nil
	}
	js, err := OpenJudgements(AuditPath(dir))
	if err != nil {
		return nil
	}
	defer js.Close()
	c, err := js.PageCorrections(docPath)
	if err != nil {
		return nil
	}
	return c
}

func writebackForDoc(docPath string, fallback bool) bool {
	return projectFlagForDoc(docPath, func(f projectFlags) *bool { return f.Writeback }, fallback)
}
