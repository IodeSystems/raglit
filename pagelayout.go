package raglit

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Showing what the model SAW, beside what the index KEPT.
//
// A layout-aware VLM returns each block with a box and a label. The index throws
// both away on purpose (see indextext.go) — but they are the only record of how
// the page was understood, and they are the thing a person needs to check a
// transcription against the pixels. Until now nothing rendered them: `bbox`
// appeared in this codebase only as a figure's location in a search result.
//
// The coordinates are NORMALISED to ~0-1000 per axis, INDEPENDENTLY — not
// pixels, and not aspect-preserving. Verified by overlay on a 2550x3300 scan
// whose boxes topped out at 924 x 934: every box landed on its line. That is
// why nothing here needs the image's dimensions, and why a renderer can place
// boxes as CSS percentages directly.

// LayoutBox is one block the model reported: where it is, what kind it thought
// it was, and what it read there.
type LayoutBox struct {
	// X0..Y1 are normalised to 0-1000 per axis, as the model emits them.
	X0, Y0, X1, Y1 int    `json:"x0"`
	Label          string `json:"label"`
	Text           string `json:"text"`
}

// Pct returns the box as CSS percentages (left, top, width, height), which is
// the whole reason the normalised space is convenient: no image dimensions, no
// aspect correction, and it stays correct at any rendered size.
func (b LayoutBox) Pct() (left, top, width, height float64) {
	return float64(b.X0) / 10, float64(b.Y0) / 10,
		float64(b.X1-b.X0) / 10, float64(b.Y1-b.Y0) / 10
}

var (
	// The opening tag is matched WHOLE and its attributes pulled out separately.
	// One pattern trying to order them does not work: with `data-bbox` first and
	// `data-label` in an optional group, the lazy run between them matches empty
	// and the label is silently skipped — every box came back unlabelled, which a
	// test caught. Attribute order is the model's choice, not ours.
	blockOpen = regexp.MustCompile(`(?is)<div\b[^>]*\bdata-bbox="[\d\s]+"[^>]*>`)
	attrBbox  = regexp.MustCompile(`(?i)\bdata-bbox="([\d\s]+)"`)
	attrLabel = regexp.MustCompile(`(?i)\bdata-label="([^"]*)"`)
	tagsOnly  = regexp.MustCompile(`<[^>]*>`)
)

// ParseLayoutBoxes extracts the blocks from a page's raw transcription.
//
// Returns nil when the text carries no boxes — a tesseract page, a text-layer
// page, or a model that does not emit them. A caller renders the text alone in
// that case; "no layout" is an ordinary answer, not an error.
func ParseLayoutBoxes(raw string) []LayoutBox {
	if !strings.Contains(raw, "data-bbox") {
		return nil
	}
	locs := blockOpen.FindAllStringIndex(raw, -1)
	out := make([]LayoutBox, 0, len(locs))
	for i, m := range locs {
		tag := raw[m[0]:m[1]]
		bb := attrBbox.FindStringSubmatch(tag)
		if bb == nil {
			continue
		}
		nums := strings.Fields(bb[1])
		if len(nums) != 4 {
			continue
		}
		var v [4]int
		bad := false
		for j, n := range nums {
			x, err := strconv.Atoi(n)
			if err != nil {
				bad = true
				break
			}
			v[j] = x
		}
		if bad {
			continue
		}
		label := ""
		if l := attrLabel.FindStringSubmatch(tag); l != nil {
			label = l[1]
		}
		// The block's text runs to the next block's opening tag — nesting is not
		// tracked because these come back flat, and a </div> hunt would mis-pair
		// on the nested <p> and <b> that are always inside.
		end := len(raw)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := strings.TrimSpace(tagsOnly.ReplaceAllString(raw[m[1]:end], " "))
		body = strings.Join(strings.Fields(body), " ")
		out = append(out, LayoutBox{X0: v[0], Y0: v[1], X1: v[2], Y1: v[3], Label: label, Text: body})
	}
	return out
}

// PageLayout is everything needed to render one page three ways: the picture,
// what the model reported about it, and what the index actually holds.
type PageLayout struct {
	Doc string `json:"doc"`
	// Page is 1-based, as everything user-facing in raglit is.
	Page int `json:"page"`
	// Engine and Model say WHO read it — a tesseract page has no boxes and that
	// is worth showing rather than looking like a failure.
	Engine string `json:"engine"`
	Model  string `json:"model"`
	// HasImage is false when the page image was never saved or has been cleaned
	// up; the text panes still work.
	HasImage bool        `json:"has_image"`
	Boxes    []LayoutBox `json:"boxes"`
	// Raw is the model's own output, markup and all. Indexed is what search
	// actually matches against. Showing both side by side is the point: they
	// have differed by 40% of their bytes and nothing surfaced it.
	Raw     string `json:"raw"`
	Indexed string `json:"indexed"`
}

// PageLayoutFor assembles one page's layout view.
//
// Raw text comes from the OCR cache, which is keyed by the image's sha256 and
// therefore survives re-ingests, model changes and the flattener — it is the
// only place the model's original output is guaranteed to still exist.
func (s *Store) PageLayoutFor(docPath string, page int) (*PageLayout, error) {
	if page < 1 {
		return nil, fmt.Errorf("raglit: page %d: pages are 1-based", page)
	}
	out := &PageLayout{Doc: docPath, Page: page}
	var imgPath string
	err := s.db.QueryRow(`SELECT o.engine, o.model, o.image_path
	                        FROM ocr_pages o JOIN documents d ON d.id = o.doc_id
	                       WHERE d.path = ? AND o.page = ?`, docPath, page).
		Scan(&out.Engine, &out.Model, &imgPath)
	if err != nil {
		return nil, fmt.Errorf("raglit: no page %d recorded for %s: %w", page, docPath, err)
	}
	out.HasImage = imgPath != ""
	if imgPath != "" {
		if raw, ok := s.cachedOCRForImageFile(imgPath); ok {
			out.Raw = raw
			out.Boxes = ParseLayoutBoxes(raw)
		}
	}
	// What the index holds for this page, in page order. Fragments are labelled
	// by their STARTING page and can span several, so this is "fragments that
	// begin here" — the honest answer, and the one that matches the review pane.
	rows, err := s.db.Query(`SELECT f.text FROM fragments f JOIN documents d ON d.id = f.doc_id
	                          WHERE d.path = ? AND f.page = ? ORDER BY f.ord`, docPath, page)
	if err == nil {
		defer rows.Close()
		var parts []string
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil {
				parts = append(parts, t)
			}
		}
		out.Indexed = strings.Join(parts, "\n\n")
	}
	return out, nil
}

// cachedOCRForImageFile finds the model's original output for a saved page
// image, by hashing the file the way the cache was keyed.
//
// Reading the file to hash it is the point: the cache key IS the image's
// sha256, so this cannot drift from what was actually read. A page whose image
// changed (a re-render at a different DPI) simply misses, which is correct — the
// old boxes describe a picture that no longer exists.
func (s *Store) cachedOCRForImageFile(imgPath string) (string, bool) {
	data, err := os.ReadFile(imgPath)
	if err != nil {
		return "", false
	}
	text, _, ok := s.cachedPageOCR(pageImageSHA(data))
	return text, ok
}
