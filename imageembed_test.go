package raglit

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseImageEmbedding(t *testing.T) {
	// Nomic shape.
	if v, err := parseImageEmbedding([]byte(`{"embeddings":[[1,2,3]]}`)); err != nil || len(v) != 3 || v[0] != 1 {
		t.Fatalf("nomic shape: %v %v", v, err)
	}
	// OpenAI-style shape.
	if v, err := parseImageEmbedding([]byte(`{"data":[{"embedding":[4,5]}]}`)); err != nil || len(v) != 2 || v[1] != 5 {
		t.Fatalf("openai shape: %v %v", v, err)
	}
	// Empty → error.
	if _, err := parseImageEmbedding([]byte(`{"embeddings":[]}`)); err == nil {
		t.Fatal("expected error for empty embeddings")
	}
}

func TestNomicVisionEmbedder_EmbedImage(t *testing.T) {
	var gotModel, gotAuth, gotCT string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		if f, _, err := r.FormFile("images"); err == nil {
			gotFile, _ = io.ReadAll(f)
			f.Close()
		}
		// Return an UN-normalized vector; the client must normalize it.
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{3, 4}}})
	}))
	defer srv.Close()

	e := NewNomicVisionEmbedder(srv.URL, "secret", "nomic-embed-vision-v1.5")
	if !e.Aligned() {
		t.Fatal("nomic-vision must report Aligned() = true")
	}
	v, err := e.EmbedImage(context.Background(), "image/png", []byte("PNGBYTES"))
	if err != nil {
		t.Fatal(err)
	}
	// [3,4] normalized → [0.6, 0.8].
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("vector not normalized: %v", v)
	}
	if gotModel != "nomic-embed-vision-v1.5" {
		t.Fatalf("model field = %q", gotModel)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Fatalf("content-type = %q", gotCT)
	}
	if string(gotFile) != "PNGBYTES" {
		t.Fatalf("uploaded file = %q", gotFile)
	}
}

// TestSearchFigures_SpaceFilter: an aligned image vector IS searchable by a text
// query; a plain (non-aligned) image vector is NOT.
func TestSearchFigures_SpaceFilter(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()

	// One document, two figures with identical billing-axis vectors but different
	// spaces: image-aligned (searchable) vs image (dormant).
	if _, err := s.db.Exec(`INSERT INTO documents(path,title,added_at) VALUES('d.pdf','D',1)`); err != nil {
		t.Fatal(err)
	}
	billing := encodeVec([]float32{0, 0, 1})
	for i, space := range []string{spaceImageAligned, spaceImage} {
		res, err := s.db.Exec(`INSERT INTO media(doc_id,page,ord,kind,description,image_path) VALUES(1,?,?,'figure',?, '')`,
			i+1, i, "a billing chart")
		if err != nil {
			t.Fatal(err)
		}
		mid, _ := res.LastInsertId()
		if _, err := s.db.Exec(`INSERT INTO media_vectors(media_id,dim,vec,space) VALUES(?,3,?,?)`, mid, billing, space); err != nil {
			t.Fatal(err)
		}
	}

	figs, err := s.SearchFigures(ctx, "billing invoice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(figs) != 1 {
		t.Fatalf("want 1 hit (aligned only), got %d", len(figs))
	}
	if figs[0].Page != 1 { // the image-aligned row was page 1
		t.Fatalf("hit page = %d, want the aligned figure (page 1)", figs[0].Page)
	}
}
