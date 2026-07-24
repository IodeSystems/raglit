package raglit

import "regexp"

// Figures explained into fragments.
//
// A scanned/diagram page needs a VLM to become text at all; while the model is
// there, the OCR prompt (figurePrompt) also DESCRIBES each figure inline, marked
// as `[FIGURE: ...]`. That description lands in the page text, flows into
// fragments unchanged, and indexes in FTS + text vectors through the existing
// path — a described diagram is searchable as text, no new infrastructure.
//
// A media row (schema.sql) then anchors each figure to the fragment holding its
// description and points at its image (a crop where one exists, else the whole
// page image raglit already saved), so a search hit arrives with its figures
// attached. The figure does NOT become its own fragment — a lone caption is
// almost always under the size floor, the exact starvation the floor prevents.

// figurePromptVersion bumps when figureInstruction changes, so frag_recipe marks
// documents re-describable after a prompt change (see fragRecipe).
const figurePromptVersion = 1

// FigurePromptVersion is the current figure-description prompt version, folded
// into recipe hashes (pool + frag_recipe) so a prompt change reprocesses the
// affected documents.
func FigurePromptVersion() int { return figurePromptVersion }

// figureInstruction is appended to the OCR prompt so the VLM inlines a compact
// description of every figure/diagram/chart it sees, in reading order, marked so
// it can be lifted back out into a media row. Kept terse: the description is for
// retrieval, not reproduction.
const figureInstruction = "\n\nFor every figure, diagram, chart, table image, or photo, insert an inline " +
	"description in reading order, on its own line, in the form " +
	"[FIGURE: <what it shows — kind, key elements, any labels/values>]. Do this in " +
	"addition to transcribing the surrounding text."

// figureRE lifts the description text out of a `[FIGURE: ...]` marker (non-greedy
// to the first closing bracket).
var figureRE = regexp.MustCompile(`\[FIGURE:\s*([^\]]*)\]`)

// parseFigureMarkers returns the description of each [FIGURE: ...] marker in text,
// in order. The markers stay in the fragment text (indexed as prose); this only
// reads them out to build media rows.
func parseFigureMarkers(text string) []string {
	ms := figureRE.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return nil
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m[1])
	}
	return out
}

// extractMedia turns the [FIGURE: ...] markers in the finalized fragments into
// media rows, anchored to the fragment (by index) that holds each marker. The
// image is the fragment's page image (the whole-page fallback: a figure whose
// crop is the page still beats no media object); precise per-figure crops are a
// later tier (see plan/fragmenters.md §3b). Ord is document-global.
func extractMedia(frags []stagedFrag, provenance []stagedPage) []stagedMedia {
	pageImg := map[int]string{}
	for _, p := range provenance {
		if p.imgPath != "" {
			pageImg[p.page] = p.imgPath
		}
	}
	var out []stagedMedia
	ord := 0
	for i, f := range frags {
		for _, desc := range parseFigureMarkers(f.text) {
			out = append(out, stagedMedia{
				fragIdx: i, page: f.page, ord: ord,
				kind: "figure", imagePath: pageImg[f.page], description: desc,
			})
			ord++
		}
	}
	return out
}
