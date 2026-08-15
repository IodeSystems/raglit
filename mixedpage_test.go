package raglit

import (
	"context"
	"strings"
	"testing"
)

// A screenshot is BOTH, and the reading has to say so.
//
// chandra reading a page of SMS messages transcribes the messages and narrates
// the phone around them — status bar, app icons, microphone and camera buttons.
// Measured on the delano SMS exhibit: 15 pages, all read by the VLM, 15% of the
// document described and 28% of its worst page. Too little for IsDescribedPage,
// far too much to record as a clean transcription. Two things were wrong and both are
// checked here, because each one alone made the number meaningless.

// screenshotPage is what a VLM returns for one page of an SMS exhibit: real
// transcribed message text, with the model's account of the phone around it.
const screenshotPage = `<div data-bbox="0 0 1000 90" data-label="Image">` +
	`<img alt="A phone status bar with the battery, wifi and signal icons, the time at the left, and a back arrow."/></div>` +
	`<div data-bbox="0 100 1000 700" data-label="Text"><p>Larry: the surveyor is coming Thursday to set the corners</p>` +
	`<p>Michele: ok I will be there, bring the plat map please</p></div>` +
	`<div data-bbox="0 710 1000 900" data-label="Image">` +
	`<img alt="A row of buttons along the bottom: a camera icon, a microphone icon, a compose icon and a blurry logo."/></div>`

// The measurement has to be taken while the markup exists, and stored — the
// index holds the FLATTENED text, so nothing downstream can recompute it. It
// was being recomputed downstream, over text that could only ever score 0.
func TestMixedPage_TheMeasurementSurvivesFlattening(t *testing.T) {
	if !IsMixedPage(screenshotPage) {
		t.Fatalf("the fixture scores %.2f — it no longer covers a mixed page", DescribedFraction(screenshotPage))
	}
	// What the index holds afterwards. No data-label, no img, nothing left to
	// measure — which is why it must be measured before this point.
	flat := FlattenForIndex(screenshotPage)
	if strings.Contains(flat, "data-label") || strings.Contains(flat, "<img") {
		t.Fatal("the fixture is not being flattened; the test proves nothing")
	}
	if got := DescribedFraction(flat); got != 0 {
		t.Fatalf("flattened text scored %.2f — expected 0, since the evidence is gone", got)
	}
}

// The per-page counts reach the page rows, and a document's fraction is the
// weighted total of them.
func TestMixedPage_PageRowsCarryTheCounts(t *testing.T) {
	s := testStore(t)
	sl := s.NewStageLog(0)
	units := []ingestUnit{
		{page: 1, text: screenshotPage},
		{page: 2, text: screenshotPage},
	}
	if _, _, err := s.ingestUnits(context.Background(), nil, nil, "file:///corpus/sms.pdf", "sms.pdf", units, FragConfig{}, sl); err != nil {
		t.Fatal(err)
	}
	_, pages, err := s.DocReview("file:///corpus/sms.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("%d page rows", len(pages))
	}
	for _, pg := range pages {
		if pg.TextChars == 0 {
			t.Fatalf("page %d recorded no text length — nothing can be weighted", pg.Page)
		}
		if got := pg.DescribedPct(); got < 20 || got > 70 {
			t.Fatalf("page %d scored %d%% described — a screenshot is a mixed page", pg.Page, got)
		}
	}
}

// The METHOD is what read the page, not what chopped it.
//
// It was taken from the fragmenter's mode, and `text-overlap` is also what a
// vision read falls back to when the LLM segmenter drops text. So a 15-page
// exhibit read entirely by chandra was recorded as `text-layer` — and that is
// not a cosmetic mislabel, it RAISES the claimed trust from a model's 90 to an
// exact 100, asserting the bytes were the text when a model made them up.
func TestMixedPage_MethodComesFromTheEngineNotTheFragmenter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const doc = "file:///corpus/sms.pdf"
	sl := s.NewStageLog(0)
	units := []ingestUnit{{page: 1, text: screenshotPage}, {page: 2, text: screenshotPage}}
	if _, _, err := s.ingestUnits(ctx, nil, nil, doc, "sms.pdf", units, FragConfig{}, sl); err != nil {
		t.Fatal(err)
	}
	// As the pipeline records a page a model read.
	if _, err := s.db.Exec(
		`UPDATE ocr_pages SET engine='vision' WHERE doc_id=(SELECT id FROM documents WHERE path=?)`, doc); err != nil {
		t.Fatal(err)
	}

	w := &Worker{Store: s}
	w.recordIngestReading(doc, "deadbeef", KindPDF, sl)

	r, ok, err := s.ReadingFor(doc)
	if err != nil || !ok {
		t.Fatalf("no reading recorded: ok=%v err=%v", ok, err)
	}
	if r.Method != MethodVision {
		t.Fatalf("method %q — every page was read by a model; text-layer would claim the bytes were the text", r.Method)
	}
	if r.DescribedPct < 20 || r.DescribedPct > 70 {
		t.Fatalf("described %d%% — the document is a screenshot exhibit", r.DescribedPct)
	}
	if r.Describes {
		t.Fatal("a mixed page is not wholly described — its transcription is real and quotable")
	}
	// And what it now reports: both claims, weakest first.
	got := strings.Join(r.TrustSummary(), " ")
	if !strings.Contains(got, "subject") || !strings.Contains(got, "text") {
		t.Fatalf("trust %q — a mixed reading makes BOTH claims and must report both", got)
	}
}

// A page with no description is still a transcription, and a document read from
// its text layer still says so.
func TestMixedPage_APlainPageIsUnaffected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const doc = "file:///corpus/order.pdf"
	sl := s.NewStageLog(0)
	units := []ingestUnit{{page: 1, text: `<div data-label="Text"><p>IT IS HEREBY ORDERED that the motion to continue is granted.</p></div>`}}
	if _, _, err := s.ingestUnits(ctx, nil, nil, doc, "order.pdf", units, FragConfig{}, sl); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s}
	w.recordIngestReading(doc, "cafe", KindPDF, sl)
	r, ok, err := s.ReadingFor(doc)
	if err != nil || !ok {
		t.Fatalf("no reading: ok=%v err=%v", ok, err)
	}
	if r.DescribedPct != 0 || r.Describes {
		t.Fatalf("a transcribed order scored %d%% described", r.DescribedPct)
	}
	if r.Method != MethodTextLayer {
		t.Fatalf("method %q — no page escalated to a model", r.Method)
	}
	if _, ok := r.Trust()[FacetSubject]; ok {
		t.Fatal("a transcribed order makes no claim about what an image depicts")
	}
}

// The other description mechanism counts too.
//
// A layout-aware read wraps descriptions in Image regions; the figure-caption
// path writes `[FIGURE: ...]` inline instead, and the corpus holds both — 768
// cached pages carry layout markup, 201 carry figure markers, no overlap.
// Counting only the first scored a page whose only description is a figure
// marker at 0%, which reads as "a model made none of this up".
func TestMixedPage_FigureMarkersAreDescription(t *testing.T) {
	page := "Wolff Hislop Crockett\n(509) 927-9700 (Phone)\n\n" +
		"[FIGURE: Logo for Wolff Hislop Crockett, the letters W, H and C intertwined in a circular design]\n\n" +
		"THE CONTENTS OF THIS ELECTRONIC MAIL ARE CONFIDENTIAL."
	if got := DescribedFraction(page); got <= 0 {
		t.Fatal("a figure marker is a model's account of an image and scored as nothing")
	}
	if !DescribableRead(page) {
		t.Fatal("a page carrying figure markers can be scored")
	}
	// A pure caption page is a description, the same as a photograph.
	if f := DescribedFraction("[FIGURE: A red Chevrolet Malibu parked on a gravel lot, plate CEP0912]"); f < describedPageThreshold {
		t.Fatalf("a page that is nothing but a figure marker scored %.2f", f)
	}
}

// "Described nothing" and "cannot tell" are different answers, and a reader
// deciding whether to quote needs them kept apart.
func TestMixedPage_AnUnmarkedModelReadIsNotScoredAtZero(t *testing.T) {
	// What an older prompt returned: transcription and description run together
	// with no seam, and nothing to say which is which.
	plain := "8:09 AM Thu Sep 29\nA phone screen showing a message thread.\nLarry: the surveyor is coming Thursday"
	if DescribableRead(plain) {
		t.Fatal("a read with no description markup was called scorable")
	}
	// A layout read of the same page CAN be scored, even at zero.
	labelled := `<div data-bbox="0 0 10 10" data-label="Text"><p>IT IS HEREBY ORDERED</p></div>`
	if !DescribableRead(labelled) {
		t.Fatal("a layout read is scorable")
	}
	if got := DescribedFraction(labelled); got != 0 {
		t.Fatalf("a wholly transcribed page scored %.2f described", got)
	}

	// And end to end: a vision page with no markup leaves the reading UNMEASURED
	// rather than claiming zero.
	s := testStore(t)
	const doc = "file:///corpus/oldread.pdf"
	sl := s.NewStageLog(0)
	if _, _, err := s.ingestUnits(context.Background(), nil, nil, doc, "oldread.pdf",
		[]ingestUnit{{page: 1, text: plain}}, FragConfig{}, sl); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE ocr_pages SET engine='vision' WHERE doc_id=(SELECT id FROM documents WHERE path=?)`, doc); err != nil {
		t.Fatal(err)
	}
	// The page rows must carry no measurement — the ingest ran as a text unit, so
	// re-measure the way a vision read would have.
	if _, err := s.db.Exec(
		`UPDATE ocr_pages SET text_chars=0, described_chars=0 WHERE doc_id=(SELECT id FROM documents WHERE path=?)`, doc); err != nil {
		t.Fatal(err)
	}
	(&Worker{Store: s}).recordIngestReading(doc, "abc", KindPDF, sl)
	r, ok, _ := s.ReadingFor(doc)
	if !ok {
		t.Fatal("no reading")
	}
	if r.DescribedPct != DescribedUnmeasured {
		t.Fatalf("described %d — an unmarked model read cannot be scored, and must not claim zero", r.DescribedPct)
	}
}
