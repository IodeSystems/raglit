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

	// Text units (a born-digital PDF's text-layer pages): segmented directly, no
	// OCR. The OCR-split path (image → ocr → segment) is covered by atomic_test.go.
	units := []ingestUnit{
		{page: 1, text: "raw text of page one"},
		{page: 2, text: "raw text of page two"},
	}
	n, err := s.ingestUnits(ctx, sg, nil, "doc.pdf", "Doc", units, nil)
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
	n, err := s.ingestUnits(context.Background(), sg, nil, "d", "", []ingestUnit{{page: 1, text: "raw"}}, nil)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if h, _ := s.Search("only fragment", 3); len(h) != 1 {
		t.Fatalf("indexed without embedder: %+v", h)
	}
}
