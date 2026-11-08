package raglit

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

// noTesseract is a cheap-OCR config whose tesseract cannot be found, so a test
// does not depend on what the machine running it has on PATH.
var noTesseract = OCRConfig{TesseractBin: filepath.Join("no", "such", "tesseract")}

// No models configured is the offline worker: blank-line text, scans fail as
// unreadable, no audio, no captions.
func TestNewWorker_NoModelsIsOffline(t *testing.T) {
	s := testStore(t)
	w := NewWorker(s, Models{OCR: noTesseract, Channels: NewChannels()}, nil)
	if w.Store != s {
		t.Fatal("worker is not on the store it was built for")
	}
	if w.OCR != nil {
		t.Errorf("OCR = %+v, want nil with no vision model and no local engine", w.OCR)
	}
	if w.STT != nil || w.Segmenter != nil || w.Retries != nil {
		t.Errorf("STT=%v Segmenter=%v Retries=%v, want all nil", w.STT, w.Segmenter, w.Retries)
	}
	if w.Pool != nil || w.RecipeHash != "" {
		t.Errorf("pool wired with no pool given: %v %q", w.Pool, w.RecipeHash)
	}
	if s.identity() != nil {
		t.Error("identifier set with no identity model")
	}
	if w.Frag.SegmentInputLimit != 0 || w.Frag.EmbedLimitTokens != 0 {
		t.Errorf("limits %+v, want unknown (0) with no models", w.Frag)
	}
}

// propsServer states its context window at /props the way llama.cpp does, and
// has no tokenizer: every other route is a 404.
func propsServer(t *testing.T, nCtx int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":` + strconv.Itoa(nCtx) + `}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A vision model turns on OCR and the segmenter, both gated on their own
// models' channels, and the probes size the fragmenter from the endpoint.
func TestNewWorker_VisionSetsOCRAndSegmenter(t *testing.T) {
	srv := propsServer(t, 8000)
	base := srv.URL + "/v1"
	s := testStore(t)
	chans := NewChannels()
	pool, err := OpenPool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	m := Models{
		Vision:   ModelRole{BaseURL: base, Model: "vis"},
		Segment:  ModelRole{BaseURL: base, Model: "seg"},
		Embed:    ModelRole{BaseURL: base, Model: "nomic-embed-text"},
		Audio:    ModelRole{BaseURL: base, Model: "stt"},
		Identity: ModelRole{Model: "seg"},
		OCR:      noTesseract,
		Channels: chans,
	}
	w := NewWorker(s, m, pool)

	if w.OCR == nil || w.OCR.Model != "vis" {
		t.Fatalf("OCR = %+v, want one reading with model vis", w.OCR)
	}
	if g, ok := w.OCR.Client.(*gatedChatter); !ok || g.model != "vis" || g.ch != chans {
		t.Errorf("OCR client %T is not gated on the vision model's channel", w.OCR.Client)
	}
	if w.Segmenter == nil {
		t.Fatal("no segmenter with a vision model configured")
	}
	if g, ok := w.Segmenter.Client.(*gatedChatter); !ok || g.model != "seg" {
		t.Errorf("segmenter client %T is not gated on the segment model", w.Segmenter.Client)
	}
	if w.Retries == nil {
		t.Error("no retry tally on a worker with model clients")
	}
	if w.STT == nil || w.STT.Model != "stt" || w.STT.Chan != chans {
		t.Errorf("STT = %+v, want model stt on the shared channels", w.STT)
	}
	// /props states 8000; the chat request keeps half for the reply, the
	// embedder takes the whole window.
	if got, want := w.Frag.SegmentInputLimit, 8000*chatInputShare/100; got != want {
		t.Errorf("segment input limit %d, want %d", got, want)
	}
	if w.Frag.EmbedLimitTokens != 8000 {
		t.Errorf("embed limit tokens %d, want 8000", w.Frag.EmbedLimitTokens)
	}
	if want := TokensToChars(8192); w.Frag.EmbedLimit != want {
		t.Errorf("embed limit chars %d, want %d (nomic's native window)", w.Frag.EmbedLimit, want)
	}
	if w.Frag.TokenCounter != nil || w.Frag.EmbedTokenCounter != nil {
		t.Error("token counters set against an endpoint with no tokenizer")
	}
	if s.identity() == nil || s.identity().Model != "seg" {
		t.Errorf("identifier = %+v, want model seg", s.identity())
	}
	if w.Pool != pool || w.RecipeHash == "" {
		t.Errorf("pool %v recipe %q, want the pool and a recipe", w.Pool, w.RecipeHash)
	}
}

// An empty Segment role is the vision model on the vision endpoint, which is
// what the segmenter always was.
func TestNewWorker_SegmentDefaultsToVision(t *testing.T) {
	srv := propsServer(t, 8000)
	s := testStore(t)
	w := NewWorker(s, Models{
		Vision:                  ModelRole{BaseURL: srv.URL + "/v1", Model: "vis"},
		SegmentInputLimitTokens: 1000,
		OCR:                     noTesseract,
		Channels:                NewChannels(),
	}, nil)
	if w.Segmenter == nil {
		t.Fatal("no segmenter")
	}
	if g, ok := w.Segmenter.Client.(*gatedChatter); !ok || g.model != "vis" {
		t.Errorf("segmenter client %T is not on the vision model", w.Segmenter.Client)
	}
	// Configured wins over the probe.
	if w.Frag.SegmentInputLimit != 1000 {
		t.Errorf("segment input limit %d, want the configured 1000", w.Frag.SegmentInputLimit)
	}
}

// A nil registry is refused, not replaced: a fresh one per worker would split
// one server's capacity between indexes and drop the caller's ceiling.
func TestNewWorker_NilChannelsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewWorker with nil Channels did not panic")
		}
	}()
	NewWorker(testStore(t), Models{OCR: noTesseract}, nil)
}
