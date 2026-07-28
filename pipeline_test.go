package raglit

import (
	"context"
	"strings"
	"testing"
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
