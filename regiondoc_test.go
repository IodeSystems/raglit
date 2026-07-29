package raglit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree builds a small read by hand: a low-resolution overview of the whole
// sheet and two blocks descended into. Shaped like the survey that made this
// necessary — the overview is where the invented text was.
func attestTree() *Region {
	root := &Region{
		Page: 1, BBox: Rect{0, 0, 1, 1}, Depth: 0, Kind: "drawing",
		Text:  "A record of survey in Havern County showing parcels A, B and C.",
		Flags: []string{FlagLowResolution}, DPI: 200, SHA256: "aa", TokensPerSqIn: 4,
		Children: []*Region{
			{Page: 1, BBox: Rect{0.05, 0.05, 0.3, 0.2}, Rotation: 90, Depth: 1, Kind: "text-block",
				Text: "THAT LIES WESTERLY OF THE CENTERLINE OF SAID RIGHT-OF-WAY",
				DPI:  200, SHA256: "bb", TokensPerSqIn: 41},
			{Page: 1, BBox: Rect{0.6, 0.7, 0.3, 0.2}, Depth: 1, Kind: "title-block",
				Text: "AF#9308270057", DPI: 200, SHA256: "cc", TokensPerSqIn: 44},
		},
	}
	root.assignIDs("p1")
	return root
}

func attestPage() RegionPage {
	root := attestTree()
	text, spans := RegionTranscript(root)
	return RegionPage{Page: 1, WidthIn: 27, HeightIn: 36.7, DPI: 200, PxW: 5400, PxH: 7340,
		Root: root, Text: text, Spans: spans}
}

// A transcription assembled from the tree keeps the interior overviews. Emitting
// only the leaves would undo the design's coverage guarantee — a region set that
// missed something is supposed to cost detail, not coverage.
func TestRegionTranscriptKeepsOverviewsAndLeaves(t *testing.T) {
	text, spans := RegionTranscript(attestTree())
	for _, want := range []string{"record of survey", "WESTERLY", "AF#9308270057"} {
		if !strings.Contains(text, want) {
			t.Errorf("assembled transcription dropped %q:\n%s", want, text)
		}
	}
	if len(spans) != 3 {
		t.Fatalf("want a span per region with text, got %d", len(spans))
	}
	// Every span must actually name the text it claims.
	for _, s := range spans {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			t.Fatalf("span %+v is not a range of a %d-byte transcription", s, len(text))
		}
	}
	if got := text[spans[1].Start:spans[1].End]; !strings.Contains(got, "WESTERLY") {
		t.Errorf("span 1 points at %q, not the block it names", got)
	}
}

// The point of the whole exercise: a passage resolves to the region that
// produced it, so the human can be shown that crop instead of the sheet.
func TestAPassageResolvesToTheRegionThatProducedIt(t *testing.T) {
	p := attestPage()

	off := strings.Index(p.Text, "WESTERLY")
	got := p.RegionAt(off)
	if got == nil {
		t.Fatal("an offset inside a transcribed block resolved to no region")
	}
	if got.ID != "p1.0" {
		t.Errorf("offset resolved to %s, want the rotated text block p1.0", got.ID)
	}
	if got.Rotation != 90 {
		t.Errorf("region %s reports rotation %d; the crop the human must see is rotated",
			got.ID, got.Rotation)
	}

	// And by quotation, which is what a consumer actually holds. Folded, because
	// the words came through a vision model and its punctuation is not evidence.
	hits := p.RegionsForQuote("lies westerly of the centerline")
	if len(hits) != 1 || hits[0].ID != "p1.0" {
		t.Errorf("quote lookup returned %v, want just p1.0", ids(hits))
	}
	if p.RegionsForQuote("no such words anywhere") != nil {
		t.Error("a quotation the page does not contain must resolve to nothing")
	}
}

// A quote in both an overview and a leaf is the same words read twice at
// different resolutions. The deeper read is the one to show.
func TestAQuoteInBothAnOverviewAndALeafPrefersTheDeeperRead(t *testing.T) {
	root := attestTree()
	root.Text = "A record of survey. AF#9308270057 appears in the title block."
	p := RegionPage{Page: 1, Root: root}
	hits := p.RegionsForQuote("AF#9308270057")
	if len(hits) != 2 {
		t.Fatalf("want both readings, got %v", ids(hits))
	}
	if hits[0].ID != "p1.1" {
		t.Errorf("deepest read must come first, got %v", ids(hits))
	}
	if hits[0].TokensPerSqIn <= hits[1].TokensPerSqIn {
		t.Error("the deeper read is supposed to be the higher-resolution one")
	}
}

// The `## Page N` headings are a contract two separate consumers parse, and the
// attribution must not touch the text they parse. Spans live in the sidecar; the
// transcription is byte for byte what it was.
func TestAttributionDoesNotTouchThePageMarkerContract(t *testing.T) {
	p := attestPage()
	md := RenderTranscription("/x/survey.pdf", []TranscribedPage{{Page: 1, Text: p.Text}})
	if !strings.Contains(md, "\n## Page 1\n") {
		t.Fatal("the page heading changed shape")
	}
	// No marker, comment or id leaked into the rendered page.
	for _, n := range p.Root.Flatten() {
		if strings.Contains(md, n.ID+"]") || strings.Contains(md, "<!-- "+n.ID) {
			t.Errorf("region id %s leaked into the transcription", n.ID)
		}
	}
	// And the page's section is the region text verbatim, which is what lets an
	// offset found in the markdown be turned into an offset into Text.
	i := strings.Index(md, "\n## Page 1\n\n")
	if i < 0 {
		t.Fatal("page section not found")
	}
	body := md[i+len("\n## Page 1\n\n"):]
	if !strings.HasPrefix(body, p.Text) {
		t.Errorf("the page section is not the assembled text verbatim:\n%q", body)
	}
	// The offset arithmetic a consumer would do must land on the right region.
	fileOff := strings.Index(md, "WESTERLY")
	got := p.RegionAt(fileOff - (i + len("\n## Page 1\n\n")))
	if got == nil || got.ID != "p1.0" {
		t.Errorf("offset lifted out of the markdown resolved to %v", got)
	}
}

// The sidecar is written beside the document, like the transcription, and a read
// of one page must not erase the record of another — the crop a quotation from
// that page was read from would go with it.
func TestSidecarIsBesideTheDocumentAndMergesPages(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "survey.pdf")
	if err := os.WriteFile(doc, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RegionsPath(doc); got != doc+".raglit-regions.json" {
		t.Errorf("sidecar path %q", got)
	}

	if _, err := WriteRegionDoc(doc, attestPage()); err != nil {
		t.Fatal(err)
	}
	three := attestPage()
	three.Page = 3
	three.Root.assignIDs("p3")
	three.Text, three.Spans = RegionTranscript(three.Root)
	if _, err := WriteRegionDoc(doc, three); err != nil {
		t.Fatal(err)
	}

	d, ok, err := ReadRegionDoc(doc)
	if err != nil || !ok {
		t.Fatalf("read back: %v (found=%v)", err, ok)
	}
	if len(d.Pages) != 2 {
		t.Fatalf("a second page read replaced the first: %d page(s)", len(d.Pages))
	}
	if d.Pages[0].Page != 1 || d.Pages[1].Page != 3 {
		t.Errorf("pages out of order: %d, %d", d.Pages[0].Page, d.Pages[1].Page)
	}
	if _, r := d.Find("p3.0"); r == nil {
		t.Error("a region id must resolve across pages without the caller parsing it")
	}
	// Re-reading a page replaces it rather than adding a duplicate.
	if _, err := WriteRegionDoc(doc, attestPage()); err != nil {
		t.Fatal(err)
	}
	if d, _, _ := ReadRegionDoc(doc); len(d.Pages) != 2 {
		t.Errorf("re-reading page 1 duplicated it: %d page(s)", len(d.Pages))
	}

	// A document with no read is not an error; most documents do not need one.
	if _, ok, err := ReadRegionDoc(filepath.Join(dir, "deed.pdf")); err != nil || ok {
		t.Errorf("a missing sidecar must report absent, not fail: %v %v", ok, err)
	}
}

// raglit must not read its own output as a source, and the sidecar is output.
func TestSidecarsAreNeverIndexedOrReRead(t *testing.T) {
	var covered bool
	for _, pat := range builtinIgnore {
		if strings.Contains(pat, regionsSuffix) {
			covered = true
		}
	}
	if !covered {
		t.Error("builtinIgnore must exclude raglit's own region sidecars")
	}
	if _, err := WriteRegionDoc("/x/survey.pdf"+regionsSuffix, attestPage()); err == nil {
		t.Error("recording regions FOR a region sidecar must be refused")
	}
	if _, err := WriteRegionDoc("/x/survey.pdf"+transcriptionSuffix, attestPage()); err == nil {
		t.Error("recording regions for a transcription must be refused")
	}
	if !IsRegionsSidecar("/x/survey.pdf" + regionsSuffix) {
		t.Error("IsRegionsSidecar should recognise its own suffix")
	}
	if IsRegionsSidecar("/x/survey.pdf") {
		t.Error("a source document is not a sidecar")
	}
}

func ids(rs []*Region) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}
