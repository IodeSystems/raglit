package raglit

import "testing"

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
