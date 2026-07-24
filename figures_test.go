package raglit

import (
	"context"
	"os"
	"testing"
)

// TestSearchFigures_DescriptionEmbeddings ingests a doc whose fragments carry
// [FIGURE: …] markers, then finds the right figure by a query semantically near
// its description — using the text embedder (no image embedder configured).
func TestSearchFigures_DescriptionEmbeddings(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()

	// Two image "pages" so they escalate to the VLM (llm-seg), each transcription
	// carrying a distinct figure marker.
	ocr := NewOCR(&okChatter{text: "body text"})
	// Descriptions use the fake embedder's vocabulary (billing vs auth axes).
	sg := NewSegmenter(&scriptChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"intro [FIGURE: an invoice billing summary table] outro"}]}`,
		`{"continues_previous":false,"fragments":[{"text":"more [FIGURE: a sequence diagram of the auth token handshake] text"}]}`,
	}})
	units := []ingestUnit{
		{page: 1, mime: "image/png", data: []byte("img1")},
		{page: 2, mime: "image/png", data: []byte("img2")},
	}
	if _, _, err := s.ingestUnits(ctx, sg, ocr, "doc.pdf", "Doc", units, FragConfig{}, nil); err != nil {
		t.Fatal(err)
	}

	// Both figures embedded (text space) → media_vectors has 2 rows.
	if n := countRows(t, s, `SELECT COUNT(*) FROM media_vectors WHERE space='text'`); n != 2 {
		t.Fatalf("media vectors = %d, want 2", n)
	}

	figs, err := s.SearchFigures(ctx, "billing invoice", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(figs) == 0 {
		t.Fatal("no figure hits")
	}
	if want := "an invoice billing summary table"; figs[0].Description != want {
		t.Fatalf("top figure = %q, want %q", figs[0].Description, want)
	}
	if figs[0].Page != 1 || figs[0].FragmentID == 0 {
		t.Fatalf("figure hit missing page/fragment anchor: %+v", figs[0])
	}
}

// stubImageEmbedder returns a fixed vector for any image.
type stubImageEmbedder struct{ called int }

func (e *stubImageEmbedder) EmbedImage(_ context.Context, _ string, _ []byte) ([]float32, error) {
	e.called++
	return []float32{1, 0, 0}, nil
}

// TestEmbedMedia_ImageEmbedderWins: when an image embedder is configured and the
// crop is on disk, the figure is embedded from the IMAGE (space "image"), not the
// description.
func TestEmbedMedia_ImageEmbedderWins(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ie := &stubImageEmbedder{}
	s.SetImageEmbedder(ie)

	f, err := os.CreateTemp(t.TempDir(), "fig-*.png")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("PNGDATA")
	f.Close()

	media := []stagedMedia{{page: 1, kind: "figure", description: "a billing table", imagePath: f.Name()}}
	s.embedMedia(context.Background(), media)
	if ie.called != 1 {
		t.Fatalf("image embedder called %d times, want 1", ie.called)
	}
	if media[0].space != "image" || len(media[0].vec) == 0 {
		t.Fatalf("media embedding = space %q, vec len %d; want image", media[0].space, len(media[0].vec))
	}
}

func TestParseFigureMarkers(t *testing.T) {
	text := "intro line\n[FIGURE: sequence diagram — client → gateway → auth]\nmore text\n" +
		"[FIGURE: bar chart of latency by region]\ntail"
	got := parseFigureMarkers(text)
	if len(got) != 2 {
		t.Fatalf("want 2 markers, got %d: %v", len(got), got)
	}
	if got[0] != "sequence diagram — client → gateway → auth" {
		t.Fatalf("marker 0 = %q", got[0])
	}
	if parseFigureMarkers("no figures here") != nil {
		t.Fatal("expected nil for text with no markers")
	}
}

func TestExtractMedia_AnchorsToFragmentAndPageImage(t *testing.T) {
	frags := []stagedFrag{
		{page: 1, ord: 0, text: "plain fragment, no figure"},
		{page: 2, ord: 0, text: "before [FIGURE: a pie chart] after"},
	}
	prov := []stagedPage{
		{page: 1, engine: "vision", imgPath: "/pages/p1.png"},
		{page: 2, engine: "vision", imgPath: "/pages/p2.png"},
	}
	media := extractMedia(frags, prov)
	if len(media) != 1 {
		t.Fatalf("want 1 media row, got %d", len(media))
	}
	m := media[0]
	if m.fragIdx != 1 || m.page != 2 || m.description != "a pie chart" {
		t.Fatalf("media = %+v", m)
	}
	if m.imagePath != "/pages/p2.png" { // whole-page fallback for page 2
		t.Fatalf("media image = %q, want the page-2 image", m.imagePath)
	}
	if m.kind != "figure" {
		t.Fatalf("kind = %q", m.kind)
	}
}
