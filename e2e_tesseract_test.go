package raglit

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// TestWorker_E2E_TesseractOnly_ImageIngest locks down the offline image path
// through the full job lifecycle: a worker built by NewWorker with ZERO LLM /
// vision / embed / identity models — only the local tesseract cheap engine —
// ingests an image to a committed index. With no vision model, NewWorker takes
// the TesseractOnlyOCR branch (w.OCR set, w.Segmenter nil), so every page is
// taken from tesseract as read (OCR.PageAsSeen, nil client) and the fragmenter
// stays the deterministic overlap one (fragModeOverlap, "text-overlap"). No
// embed/identity stage fires. The job completes, fragments land, Mode reports
// "text-overlap", and the ocr stage records engine "tesseract".
//
// Guarded: tesseract must be on PATH, else the test skips (the same pattern as
// TestNewWorker_NoModelsIsOffline, which uses a deliberately-missing binary).
func TestWorker_E2E_TesseractOnly_ImageIngest(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH; the offline image path needs the local engine")
	}
	ctx := context.Background()
	s := openMem(t)

	// Zero models: no Vision, no Segment, no Embed, no Audio, no Identity. The
	// only OCR knob is the default tesseract config (empty bin → "tesseract" on
	// PATH, eng). NewChannels() is required (NewWorker panics on nil).
	w := NewWorker(s, Models{OCR: OCRConfig{}, Channels: NewChannels()}, nil)
	if w.OCR == nil || w.OCR.Cheap == nil || w.OCR.Cheap.Name() != "tesseract" {
		t.Fatalf("worker.OCR.Cheap = %+v, want the tesseract engine", w.OCR)
	}
	if w.OCR.Client != nil {
		t.Fatalf("OCR.Client = %v, want nil (no vision model)", w.OCR.Client)
	}
	if w.Segmenter != nil {
		t.Fatalf("Segmenter = %v, want nil with no vision model", w.Segmenter)
	}

	// A real PNG of a phrase, large enough to be a page (past
	// imageDocumentFloorPx) and clear enough for tesseract to read.
	const phrase = "The quarterly report shows revenue grew twelve percent."
	pngPath := renderTextPNG(t, t.TempDir(), phrase)
	if _, err := s.Enqueue("file://"+pngPath, ""); err != nil {
		t.Fatal(err)
	}

	processed, err := w.ProcessOne(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("ProcessOne did not claim a job")
	}

	jobs, err := s.Jobs("all", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].State != "done" {
		t.Fatalf("jobs = %+v, want one done job", jobs)
	}
	if jobs[0].Fragments == 0 {
		t.Fatalf("job has no fragments: %+v", jobs[0])
	}
	if jobs[0].Mode != "text-overlap" {
		t.Fatalf("mode = %q, want text-overlap", jobs[0].Mode)
	}

	// The ocr stage must record the local engine, not a vision model.
	stages, err := s.JobStages(jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var ocrEngine string
	for _, st := range stages {
		if st.Name == "ocr" {
			ocrEngine = st.Engine
		}
	}
	// engineSummary renders the per-page engine map, e.g. "tesseract×1". It must
	// name the local engine, never a vision model.
	if !strings.Contains(ocrEngine, "tesseract") {
		t.Fatalf("ocr stage engine = %q, want tesseract (stages %+v)", ocrEngine, stages)
	}

	// The phrase landed in the index: search for it.
	hits, err := s.Search("quarterly report", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("ingested image text is not searchable")
	}
}

// renderTextPNG draws phrase onto a white PNG (real TTF, large enough for OCR
// and past the page floor) and returns its path.
func renderTextPNG(t *testing.T, dir, phrase string) string {
	t.Helper()
	ttf, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: 40, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	// 1100x320 = 352,000 px, above imageDocumentFloorPx (250,000).
	img := image.NewRGBA(image.Rect(0, 0, 1100, 320))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.Black), Face: face, Dot: fixed.P(20, 160)}
	d.DrawString(phrase)

	p := filepath.Join(dir, "page.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}
