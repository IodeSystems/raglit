package raglit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

func TestIngestUnits_SegmentedWithContinuationAndEmbed(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()

	// Fragments padded past the size floor so each stands alone (isolating the
	// continuation behavior from the small-sibling merge).
	pad := " " + strings.Repeat("lorem ipsum dolor sit amet ", 200)
	// Two "page" units. Page 2's first fragment continues page 1's open (funcB).
	sg := NewSegmenter(&scriptChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"funcA mints a token` + pad + `"},{"text":"funcB rotates the refresh token` + pad + `"}]}`,
		`{"continues_previous":true,"fragments":[{"text":"and revokes the old one` + pad + `"},{"text":"funcC flips the load balancer` + pad + `"}]}`,
	}})

	// Image units so the pages escalate to the VLM (llm-seg) — the path the
	// Assembler's cross-page continuation lives on. (Text units take the
	// deterministic overlap fragmenter, no segmenter; covered separately.)
	ocr := NewOCR(&okChatter{text: "page text"})
	units := []ingestUnit{
		{page: 1, mime: "image/png", data: []byte("img1")},
		{page: 2, mime: "image/png", data: []byte("img2")},
	}
	n, _, err := s.ingestUnits(ctx, sg, ocr, "doc.pdf", "Doc", units, FragConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// funcA (closed) + funcB+continuation (merged, closed) + funcC (closed at end) = 3.
	if n != 3 {
		t.Fatalf("want 3 fragments, got %d", n)
	}

	// Continuation merged: the funcB fragment carries page-2 text but keeps page 1.
	hits, _ := s.Search("revokes old", 5)
	if len(hits) == 0 {
		t.Fatal("merged continuation not searchable")
	}
	if hits[0].Page != 1 {
		t.Errorf("merged fragment should keep its start page (1), got %d", hits[0].Page)
	}

	// funcC landed on page 2.
	if h, _ := s.Search("load balancer", 5); len(h) == 0 || h[0].Page != 2 {
		t.Fatalf("funcC should be page 2: %+v", h)
	}

	// Every fragment got a vector via the concurrent embed pipeline.
	var vecs int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM fragment_vectors`).Scan(&vecs); err != nil {
		t.Fatal(err)
	}
	if vecs != 3 {
		t.Fatalf("want 3 vectors embedded, got %d", vecs)
	}

	// Vector search works over the segmented fragments too.
	if h, err := s.VecSearch(ctx, "token refresh", 3); err != nil || len(h) == 0 {
		t.Fatalf("vec search over segmented doc: %v / %d", err, len(h))
	}
}

func TestIngestUnits_NoEmbedderStillIndexes(t *testing.T) {
	s := openMem(t)
	sg := NewSegmenter(&scriptChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"only fragment here"}]}`,
	}})
	ocr := NewOCR(&okChatter{text: "page text"})
	n, _, err := s.ingestUnits(context.Background(), sg, ocr, "d", "", []ingestUnit{{page: 1, mime: "image/png", data: []byte("img")}}, FragConfig{}, nil)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if h, _ := s.Search("only fragment", 3); len(h) != 1 {
		t.Fatalf("indexed without embedder: %+v", h)
	}
}

// The overlap path is the one that runs without a vision segmenter, and it had
// the same defect as the assembler: it computed where every page began, used
// that to pick a START page, and discarded the rest. An overlap window is sized
// in characters and ignores page breaks, so most fragments in a multi-page
// document straddle one.
func TestOverlapFragmentsCarryPageBoundaries(t *testing.T) {
	pages := []resolvedPage{
		{page: 1, text: strings.Repeat("alpha ", 300)},
		{page: 2, text: strings.Repeat("bravo ", 300)},
		{page: 3, text: strings.Repeat("charlie ", 300)},
	}
	var got []stagedFrag
	fragmentOverlap(pages, 2000, 1500, 200, func(f stagedFrag) { got = append(got, f) })
	if len(got) == 0 {
		t.Fatal("no fragments produced")
	}
	var spanning int
	for _, f := range got {
		if len(f.pageSpans) < 2 {
			continue
		}
		spanning++
		if f.pageSpans[0].Off != 0 {
			t.Errorf("first boundary must be offset 0, got %+v", f.pageSpans[0])
		}
		for i := 1; i < len(f.pageSpans); i++ {
			s := f.pageSpans[i]
			if s.Off <= f.pageSpans[i-1].Off {
				t.Errorf("boundaries must advance: %+v", f.pageSpans)
			}
			if s.Off > len(f.text) {
				t.Errorf("boundary %d is past the fragment text (%d)", s.Off, len(f.text))
			}
			// The offset must land on that page's actual words.
			want := map[int]string{1: "alpha", 2: "bravo", 3: "charlie"}[s.Page]
			if tail := f.text[s.Off:]; !strings.HasPrefix(strings.TrimSpace(tail), want) {
				t.Errorf("boundary for page %d does not land on %q: %.30q", s.Page, want, tail)
			}
		}
	}
	if spanning == 0 {
		t.Fatal("three pages under a 2000-char window must produce a page-spanning fragment")
	}
	// And the resolution the column exists for.
	for _, f := range got {
		if len(f.pageSpans) >= 2 {
			last := f.pageSpans[len(f.pageSpans)-1]
			if p := PageAt(f.page, f.pageSpans, last.Off+1); p != last.Page {
				t.Errorf("offset past the last boundary should be page %d, got %d", last.Page, p)
			}
			break
		}
	}
}

// End-to-end for the column: boundaries computed by the fragmenter must survive
// the commit and come back out of the database.
//
// The unit tests above prove the fragmenters COMPUTE spans; this proves they are
// persisted, which is the half that a schema change and a regenerated INSERT can
// silently get wrong.
func TestPageBoundariesRoundTripThroughTheStore(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pages := []resolvedPage{
		{page: 1, text: strings.Repeat("alpha ", 300)},
		{page: 2, text: strings.Repeat("bravo ", 300)},
		{page: 3, text: strings.Repeat("charlie ", 300)},
	}
	var frags []stagedFrag
	fragmentOverlap(pages, 2000, 1500, 200, func(f stagedFrag) { frags = append(frags, f) })
	var want int
	for _, f := range frags {
		if len(f.pageSpans) >= 2 {
			want++
		}
	}
	if want == 0 {
		t.Fatal("fixture produced no page-spanning fragment")
	}
	if err := s.commitDoc("/tmp/doc.pdf", "doc", "overlap", "", frags, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.Query(`SELECT page, text, page_spans FROM fragments ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got int
	for rows.Next() {
		var page int
		var text, raw string
		if err := rows.Scan(&page, &text, &raw); err != nil {
			t.Fatal(err)
		}
		spans := DecodePageSpans(raw)
		if len(spans) < 2 {
			continue
		}
		got++
		// The stored offsets must still land on the right page's words.
		for _, sp := range spans[1:] {
			want := map[int]string{1: "alpha", 2: "bravo", 3: "charlie"}[sp.Page]
			if tail := strings.TrimSpace(text[sp.Off:]); !strings.HasPrefix(tail, want) {
				t.Errorf("stored boundary for page %d lands on %.20q, want %q", sp.Page, tail, want)
			}
		}
		// And an offset inside the last page resolves to it.
		last := spans[len(spans)-1]
		if p := PageAt(page, spans, last.Off+1); p != last.Page {
			t.Errorf("PageAt after the last boundary = %d, want %d", p, last.Page)
		}
	}
	if got != want {
		t.Errorf("%d page-spanning fragments were computed but %d came back from the DB", want, got)
	}
}

// flakyOCR fails the Nth page once, then succeeds on every later call. It counts
// how many times the model was actually asked, which is what the cache changes.
type flakyOCR struct {
	failOnCall int
	calls      int
}

func (f *flakyOCR) ChatStream(_ context.Context, _ []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	f.calls++
	if f.calls == f.failOnCall {
		return nil, fmt.Errorf("llm: status 500 (after 4 retries)")
	}
	return streamReply(fmt.Sprintf("page text %d", f.calls)), nil
}

func imageUnits(n int) []ingestUnit {
	u := make([]ingestUnit, 0, n)
	for i := 1; i <= n; i++ {
		// Distinct bytes per page, so each has its own cache key.
		u = append(u, ingestUnit{page: i, mime: "image/png", data: []byte(fmt.Sprintf("PNGPAGE%03d", i))})
	}
	return u
}

// The failure this exists for: a document that dies partway used to discard
// every page it had already transcribed, and the retry started at page 1.
func TestIngestResumesFromTheOCRCacheAfterAFailure(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	units := imageUnits(6)
	flaky := &flakyOCR{failOnCall: 5}
	ocr := NewOCR(flaky)
	// A SEPARATE client for segmentation, so flaky.calls counts OCR only. Sharing
	// one stub made the segmenter's calls look like re-read pages.
	seg := NewSegmenter(&stubChatter{reply: `{"continues_previous":false,"fragments":[{"text":"t"}]}`})

	// First run dies on page 5.
	if _, _, err := s.ingestUnits(context.Background(), seg, ocr,
		"", "doc", units, FragConfig{}, nil); err == nil {
		t.Fatal("expected the ingest to fail on page 5")
	}
	firstRunCalls := flaky.calls
	if firstRunCalls != 5 {
		t.Fatalf("expected 5 model calls before the failure, got %d", firstRunCalls)
	}

	// The four pages that succeeded must be on disk.
	var cached int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ocr_page_cache`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 4 {
		t.Fatalf("want 4 pages cached after failing on the 5th, got %d", cached)
	}

	// Second run must ask the model only for the pages it never got.
	before := flaky.calls
	if _, _, err := s.ingestUnits(context.Background(), seg, ocr,
		"", "doc", units, FragConfig{}, nil); err != nil {
		t.Fatalf("the retry should succeed: %v", err)
	}
	// Pages 1-4 come from the cache; only 5 and 6 reach the model.
	if reread := flaky.calls - before; reread != 2 {
		t.Errorf("the retry made %d OCR calls; want 2 (pages 5 and 6 only)", reread)
	}
}

// A page is keyed by its IMAGE, so the same bytes under a different document or
// page number are not re-read, and CHANGED bytes are.
func TestOCRCacheIsKeyedOnThePageImage(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stub := &stubChatter{reply: "transcribed"}
	ocr := NewOCR(stub)

	img := PageImage{Page: 1, Mime: "image/png", Data: []byte("SAMEBYTES")}
	if _, _, cached, err := s.ocrPageCached(context.Background(), ocr, img); err != nil || cached {
		t.Fatalf("first read should miss the cache (cached=%v err=%v)", cached, err)
	}
	// Same bytes, different page number and document: still the same page.
	again := PageImage{Page: 9, Mime: "image/png", Data: []byte("SAMEBYTES")}
	txt, _, cached, err := s.ocrPageCached(context.Background(), ocr, again)
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Error("identical page bytes were read twice")
	}
	if txt != "transcribed" {
		t.Errorf("cached text = %q", txt)
	}
	// Different bytes must miss — a page that renders differently is a new page.
	other := PageImage{Page: 1, Mime: "image/png", Data: []byte("OTHERBYTES")}
	if _, _, cached, _ := s.ocrPageCached(context.Background(), ocr, other); cached {
		t.Error("different page bytes hit the cache")
	}
}

// An empty transcription is not cached: a page that genuinely has no text is
// indistinguishable here from one whose OCR returned nothing because the
// upstream was unwell, and caching the second makes a transient failure
// permanent.
func TestOCRCacheDoesNotStoreAnEmptyTranscription(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	empty := &stubChatter{reply: "   \n  "}
	if _, _, _, err := s.ocrPageCached(context.Background(), NewOCR(empty),
		PageImage{Page: 1, Mime: "image/png", Data: []byte("BLANKPAGE")}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ocr_page_cache`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("an empty transcription was cached (%d rows)", n)
	}
}
