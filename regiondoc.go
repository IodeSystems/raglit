package raglit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// The region read, made durable.
//
// The descent happens, produces a tree, and today the tree is printed and thrown
// away — only flattened text survives. That is enough to search and not enough
// to ATTEST: a passage transcribed from a rotated, zoomed crop cannot be checked
// against the whole page, because the whole page is not what produced it.
//
// So the tree is written beside the document, exactly the way a transcription
// is: `<doc>.raglit-regions.json`, a suffix that says what wrote it, so a
// re-read is detectable and nobody mistakes it for a hand-made note. JSON rather
// than markdown because a tree of geometry is data — there is no reading order
// in a drawing to render as prose, which is the reason regions exist at all.
const regionsSuffix = ".raglit-regions.json"

// regionDocVersion is the sidecar's shape, so a consumer that reads one written
// by an older raglit can say so instead of guessing.
const regionDocVersion = 1

// RegionsPath is where a document's region reads live: beside it, like the
// transcription.
func RegionsPath(docPath string) string { return docPath + regionsSuffix }

// IsRegionsSidecar reports whether a path is raglit's own region output.
func IsRegionsSidecar(path string) bool { return strings.HasSuffix(path, regionsSuffix) }

// RegionDoc is a document's region reads — one tree per page that was read this
// way. Not every page is: the descent is for a sheet whose text is physically
// too small to survive one look, and a letter page does not need it.
type RegionDoc struct {
	Doc     string       `json:"doc"` // base name, for a human opening the file
	Version int          `json:"version"`
	Pages   []RegionPage `json:"pages"`
}

// RegionPage is one page's read: the tree, the transcription assembled from it,
// and which region each part of that transcription came from.
type RegionPage struct {
	Page int `json:"page"`
	// WidthIn, HeightIn are the sheet's physical size — the input that decides
	// everything here, because the encoder's budget is per image and square
	// inches are what resolution is spent on.
	WidthIn  float64 `json:"width_in,omitempty"`
	HeightIn float64 `json:"height_in,omitempty"`
	// DPI, PxW, PxH describe the page rasterization every region was cropped out
	// of. Recorded together on purpose: dpi alone says what was ASKED for, and
	// the pixel dimensions say what was produced. A renderer that disagrees about
	// the second while agreeing about the first is caught here rather than
	// discovered as an unexplained digest mismatch.
	DPI  int     `json:"dpi"`
	PxW  int     `json:"px_w,omitempty"`
	PxH  int     `json:"px_h,omitempty"`
	Root *Region `json:"root"`
	// Text is this page's transcription, assembled from the tree. Byte for byte
	// what belongs under `## Page N` in the transcription sidecar.
	Text string `json:"text"`
	// Spans map byte ranges of Text to the region that produced them.
	//
	// Out here rather than inline in the markdown, and that is the load-bearing
	// choice: `## Page N` is a contract two separate consumers already parse, and
	// a marker interleaved with the text would land INSIDE the quotations they
	// match against. The transcription stays exactly what it was; the attribution
	// lives in a file nothing else reads.
	Spans []RegionSpan `json:"spans,omitempty"`
}

// RegionSpan attributes a byte range of a page's transcription to a region.
type RegionSpan struct {
	Region string `json:"region"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// RegionTranscript assembles a page's transcription from its region tree, and
// says which region every part of it came from.
//
// Parents before children in reading order — Flatten's order, which is also the
// order the tree prints in. EVERY node's text is emitted, not only the leaves:
// an interior node describes the whole of what is under it, and that is the
// design's coverage guarantee — a bad region set is supposed to cost detail
// rather than coverage. Dropping the overviews here would put the loss straight
// back.
//
// It does duplicate: an overview and the leaves that refine it describe the same
// block twice. That is the honest artifact, and it is also why the spans matter.
// An overview read at four tokens per square inch is precisely where invented
// text lives, and a reader who can see WHICH region a sentence came from can see
// that it came from a low-resolution one — and ask for that crop.
//
// Whether leaves should replace fragments outright is still open; see
// plan/hierarchical-regions.md. This assembles a transcription and decides
// nothing about fragmentation.
func RegionTranscript(root *Region) (string, []RegionSpan) {
	if root == nil {
		return "", nil
	}
	var b strings.Builder
	var spans []RegionSpan
	for _, n := range root.Flatten() {
		t := strings.TrimSpace(n.Text)
		if t == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		start := b.Len()
		b.WriteString(t)
		spans = append(spans, RegionSpan{Region: n.ID, Start: start, End: b.Len()})
	}
	return b.String(), spans
}

// RegionAt reports the region a byte offset in the page's transcription came
// from, or nil if the offset falls between spans.
//
// The offset is into RegionPage.Text. A consumer that located a quotation in the
// markdown sidecar has an offset into the FILE; subtract the start of the page's
// section — the text under `## Page N` is this text verbatim — or use
// RegionsForQuote, which does not need offsets at all.
func (p RegionPage) RegionAt(off int) *Region {
	for _, s := range p.Spans {
		if off >= s.Start && off < s.End {
			return p.Root.FindByID(s.Region)
		}
	}
	return nil
}

// RegionsForQuote reports the regions whose text contains a quotation, deepest
// first.
//
// Deepest first because a deeper region was read at a narrower field of view,
// which is the higher-resolution reading of the same words and therefore the one
// to show. A quotation matching only an interior node is a quotation nothing
// looked at closely — worth knowing, and the flags on that node will usually say
// why.
//
// Matched on folded text — letters and digits, lowercased — because the words
// went through a vision model and hyphenation, spacing and punctuation are
// exactly what it renders inconsistently. A consumer that located the quote in
// the transcription folded it the same way to find it there.
func (p RegionPage) RegionsForQuote(quote string) []*Region {
	needle := foldForMatch(quote)
	if needle == "" || p.Root == nil {
		return nil
	}
	var out []*Region
	for _, n := range p.Root.Flatten() {
		if strings.Contains(foldForMatch(n.Text), needle) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Depth > out[j].Depth })
	return out
}

// foldForMatch reduces text to letters and digits, lowercased.
func foldForMatch(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// Find resolves a region id anywhere in the document. The id carries its page
// ("p3.1"), but a caller holding an id from a citation should not have to parse
// it to use it.
func (d *RegionDoc) Find(id string) (RegionPage, *Region) {
	for _, p := range d.Pages {
		if p.Root == nil {
			continue
		}
		if r := p.Root.FindByID(id); r != nil {
			return p, r
		}
	}
	return RegionPage{}, nil
}

// PageRead returns the read for a page number, if this document has one.
func (d *RegionDoc) PageRead(n int) (RegionPage, bool) {
	for _, p := range d.Pages {
		if p.Page == n {
			return p, true
		}
	}
	return RegionPage{}, false
}

// ReadRegionDoc loads the sidecar beside a document. A missing sidecar is not an
// error — most documents are not read this way — and reports false.
func ReadRegionDoc(docPath string) (*RegionDoc, bool, error) {
	b, err := os.ReadFile(RegionsPath(docPath))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var d RegionDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, false, fmt.Errorf("raglit: %s: %w", RegionsPath(docPath), err)
	}
	if d.Version > regionDocVersion {
		return nil, false, fmt.Errorf("raglit: %s was written by a newer raglit (version %d, this one reads %d)",
			filepath.Base(RegionsPath(docPath)), d.Version, regionDocVersion)
	}
	return &d, true, nil
}

// WriteRegionDoc merges page reads into the sidecar beside a document.
//
// Merges rather than replaces: `raglit regions` reads ONE page, and a run over
// page 7 must not delete the record of page 3 — the crop that page 3's quotation
// was read from would go with it, and the quotation would still be in the
// filing.
func WriteRegionDoc(docPath string, pages ...RegionPage) (string, error) {
	// Never read a sidecar as a source. Discovery keeps these out, but an
	// explicit `raglit regions <file>` bypasses discovery, and one slip starts
	// writing x.raglit-regions.json.raglit-regions.json.
	if IsRegionsSidecar(docPath) || IsTranscription(docPath) {
		return "", fmt.Errorf("raglit: %s is raglit's own output; refusing to record regions for it",
			filepath.Base(docPath))
	}
	doc, _, err := ReadRegionDoc(docPath)
	if err != nil {
		return "", err
	}
	if doc == nil {
		doc = &RegionDoc{}
	}
	doc.Doc = filepath.Base(docPath)
	doc.Version = regionDocVersion
	for _, p := range pages {
		replaced := false
		for i := range doc.Pages {
			if doc.Pages[i].Page == p.Page {
				doc.Pages[i], replaced = p, true
				break
			}
		}
		if !replaced {
			doc.Pages = append(doc.Pages, p)
		}
	}
	sort.SliceStable(doc.Pages, func(i, j int) bool { return doc.Pages[i].Page < doc.Pages[j].Page })

	// Indented and page-ordered so two reads of the same sheet diff as a change
	// in what was seen rather than as formatting noise.
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	out := RegionsPath(docPath)
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	return out, nil
}
