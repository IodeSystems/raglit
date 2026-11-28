package raglit

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// stubChatter records what the OCR path sent and returns a canned transcription.
type stubChatter struct {
	sawImage bool
	sawText  bool
	called   bool // the model was invoked (the cascade reached the VLM)
	dataURI  string
	reply    string
}

func (s *stubChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	s.called = true
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch p.Type {
			case "image_url":
				if p.ImageURL != nil {
					s.sawImage = true
					s.dataURI = p.ImageURL.URL
				}
			case "text":
				s.sawText = true
			}
		}
	}
	return streamReply(s.reply), nil
}

func TestOCR_Page_SendsMultimodalAndTrims(t *testing.T) {
	sc := &stubChatter{reply: "  Refresh token rotates.  \n"}
	got, err := NewOCR(sc).Page(context.Background(), PageImage{
		Page: 2, Mime: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.sawImage {
		t.Error("OCR did not attach an image part")
	}
	if !sc.sawText {
		t.Error("OCR did not include a text instruction")
	}
	if !strings.HasPrefix(sc.dataURI, "data:image/png;base64,") {
		t.Errorf("image not sent as a png data URI: %q", sc.dataURI)
	}
	if got != "Refresh token rotates." {
		t.Errorf("result not trimmed: %q", got)
	}
}

// scriptedChatter replays a sequence of turns, so a test can say "loop, then
// return this" or "overflow, then succeed" and assert what the page path did.
type scriptedChatter struct {
	turns  []scriptedTurn
	n      int
	sizes  []int // bytes of image data received, per call
	widths []int
}

type scriptedTurn struct {
	text    string
	repeats bool  // finish with a repetition stop reason
	err     error // stream-level error (an API refusal)
}

func (s *scriptedChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == "image_url" && p.ImageURL != nil {
				s.sizes = append(s.sizes, len(p.ImageURL.URL))
				if img, err := png.Decode(base64.NewDecoder(base64.StdEncoding,
					strings.NewReader(dataURIPayload(p.ImageURL.URL)))); err == nil {
					s.widths = append(s.widths, img.Bounds().Dx())
				}
			}
		}
	}
	t := s.turns[min(s.n, len(s.turns)-1)]
	s.n++
	ch := make(chan llm.StreamChunk, 4)
	go func() {
		defer close(ch)
		if t.err != nil {
			ch <- llm.StreamChunk{Error: t.err.Error()}
			return
		}
		ch <- llm.StreamChunk{Content: t.text}
		if t.repeats {
			ch <- llm.StreamChunk{StopReason: llm.StopReasonRepetition,
				Repetition: &llm.RepetitionInfo{Where: "content", Period: 28, Reps: 23}}
		}
	}()
	return ch, nil
}

func dataURIPayload(u string) string {
	if i := strings.Index(u, ","); i >= 0 {
		return u[i+1:]
	}
	return u
}

// testPNG is a real decodable PNG of the given size, so the downscale path runs
// for real rather than against a 4-byte stub.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The failure this guard exists for. A recorded survey looped on the first
// pass; the loop-break retry came back tidy and well-formed, having dropped the
// entire legal description and invented auditor file numbers. It read as a
// complete transcription and the job reported success.
func TestOCR_LoopBreakRetryShorterThanTheCutIsRejected(t *testing.T) {
	sc := &scriptedChatter{turns: []scriptedTurn{
		{text: strings.Repeat("THE EASTERLY 25 FEET WESTERLY OF THE CENTERLINE. ", 40), repeats: true},
		{text: "[FIGURE: Survey plat map.]"}, // clean, plausible, and missing the page
	}}
	_, err := NewOCR(sc).Page(context.Background(), PageImage{
		Page: 1, Mime: "image/png", Data: testPNG(t, 40, 40),
	})
	if err == nil {
		t.Fatal("a retry that dropped the page's text was accepted as a transcription")
	}
	if !strings.Contains(err.Error(), "NOT indexed") {
		t.Errorf("the error must say the page was not indexed, got: %v", err)
	}
}

// A retry that genuinely recovers the page is longer than the cut, and must be
// kept — otherwise the loop-break retry would be pointless.
func TestOCR_LoopBreakRetryLongerThanTheCutIsKept(t *testing.T) {
	sc := &scriptedChatter{turns: []scriptedTurn{
		{text: "PARCEL A: THE EAST", repeats: true},
		{text: "PARCEL A: THE EASTERLY 25 FEET ... THAT LIES WESTERLY OF THE CENTERLINE."},
	}}
	got, err := NewOCR(sc).Page(context.Background(), PageImage{
		Page: 1, Mime: "image/png", Data: testPNG(t, 40, 40),
	})
	if err != nil {
		t.Fatalf("a recovered page was rejected: %v", err)
	}
	if !strings.Contains(got, "WESTERLY OF THE CENTERLINE") {
		t.Errorf("the recovered transcription was not returned, got %q", got)
	}
}

// Over a context limit by a few dozen tokens is not a document problem, and
// retrying it unchanged fails identically forever.
func TestOCR_ContextOverflowRetriesWithASmallerImage(t *testing.T) {
	sc := &scriptedChatter{turns: []scriptedTurn{
		{err: errors.New(`llm: status 400: {"error":{"message":"request (180273 tokens) exceeds the available context size (180224 tokens)","type":"exceed_context_size_error"}}`)},
		{text: "PAGE TEXT RECOVERED AT LOWER RESOLUTION"},
	}}
	got, err := NewOCR(sc).Page(context.Background(), PageImage{
		Page: 1, Mime: "image/png", Data: testPNG(t, 400, 400),
	})
	if err != nil {
		t.Fatalf("an oversized page was not retried smaller: %v", err)
	}
	if got != "PAGE TEXT RECOVERED AT LOWER RESOLUTION" {
		t.Errorf("got %q", got)
	}
	if len(sc.widths) < 2 {
		t.Fatalf("expected two attempts, saw %d images", len(sc.widths))
	}
	if sc.widths[1] >= sc.widths[0] {
		t.Errorf("the retry must send a SMALLER image: %d then %d", sc.widths[0], sc.widths[1])
	}
}

// Downscaling is bounded. A page that never fits fails with the model's own
// reason rather than shrinking until the text is unreadable.
func TestOCR_ContextOverflowGivesUpRatherThanShrinkingForever(t *testing.T) {
	boom := errors.New("llm: status 400: exceeds the available context size")
	sc := &scriptedChatter{turns: []scriptedTurn{{err: boom}}}
	_, err := NewOCR(sc).Page(context.Background(), PageImage{
		Page: 1, Mime: "image/png", Data: testPNG(t, 400, 400),
	})
	if err == nil {
		t.Fatal("a page that never fits must fail")
	}
	if n := len(sc.widths); n != maxContextShrinks+1 {
		t.Errorf("want %d attempts, got %d", maxContextShrinks+1, n)
	}
}

// The assist hands the vision model WORDS and withholds NUMBERS.
//
// Measured on a disputed record of survey: given tesseract's full text, the
// model adopted its misread certificate number (20123164) over the correct one
// it had produced unaided (20123169), and telling it to prefer the image for
// digits did not change that. With the digits removed there is nothing wrong to
// copy, and the spelling anchor still lands — that configuration read all four
// checked facts correctly, including one auditor's file number no other
// configuration read right.
func TestSpellingAssist_KeepsWordsAndRemovesNumbers(t *testing.T) {
	const tess = "INSCRIBED DACE 20123164.\nAUDITOR'S FILE NUMBER 201907220015 WILL NEED\nFEE $292.50 ON 05/23/2022"
	got := spellingAssist(tess)
	for _, want := range []string{"INSCRIBED", "DACE", "AUDITOR'S FILE NUMBER", "numbers removed"} {
		if !strings.Contains(got, want) {
			t.Errorf("assist dropped %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"20123164", "201907220015", "292.50", "05/23/2022", "2022"} {
		if strings.Contains(got, gone) {
			t.Errorf("assist leaked the number %q — the model will copy it:\n%s", gone, got)
		}
	}
	if spellingAssist("  ") != "" {
		t.Error("an empty page should offer no assist")
	}
	if spellingAssist("20123164 201907220015 05/23/2022") != "" {
		t.Error("a page of nothing but numbers offers no spellings, so it should offer no assist")
	}
}

// The born-digital half of the figure gate: a PDF page carrying an embedded
// image XObject is escalated to the VLM when DescribeFigures is on, even
// though the raster heuristic (figureLikeImage) does NOT flag the render —
// a modest image on a mostly-text page is exactly the class the XObject
// trigger exists for.
//
// The fixture is built with the same pdfcpu ImportImagesFile pattern as
// pagify_test.go, but sized so figureLikeImage returns false (a small white
// image). The test then exercises the gate at the PageAsSeen level with
// HasEmbeddedImage set, and the threading at the pdfUnits → unitsToPageText
// level.
func TestFigureGateBornDigitalEmbeddedImageEscalates(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "small.png")
	writePNG(t, pngPath, 64, 64) // small white image — not figure-like

	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := api.ImportImagesFile(context.Background(), []string{pngPath}, pdfPath,
		pdfcpu.DefaultImportConfig(), model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("build fixture pdf: %v", err)
	}

	// The extracted image must NOT be figure-like by the raster heuristic —
	// that is the precondition for the XObject path to be the only trigger.
	pages, err := Pagify(pdfPath, "")
	if err != nil {
		t.Fatalf("pagify: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("want 1 extracted page, got %d", len(pages))
	}
	if fig, _, _ := figureLikeImage(pages[0].Data); fig {
		t.Fatal("fixture image is figure-like by the raster heuristic — the XObject " +
			"trigger cannot be tested in isolation")
	}

	cleanPO := PageOCR{Text: "Quarterly report.", MeanConfidence: 0.97, BoxCount: 3}

	t.Run("Xobject flag escalates when DescribeFigures on", func(t *testing.T) {
		vlm := &stubChatter{reply: "[FIGURE: small embedded diagram]"}
		o := &OCR{Client: vlm, Cheap: stubEngine{"tesseract", cleanPO, nil}, DescribeFigures: true}
		text, engine, _, err := o.PageAsSeen(t.Context(), PageImage{
			Page: 1, Mime: "image/png", Data: pages[0].Data, HasEmbeddedImage: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if engine != "vision" {
			t.Errorf("engine = %q, want vision (vlm called %v)", engine, vlm.called)
		}
		if !vlm.called {
			t.Error("VLM was not called despite the XObject flag")
		}
		if text != "[FIGURE: small embedded diagram]" {
			t.Errorf("text = %q, want the VLM's figure description", text)
		}
	})

	t.Run("Xobject flag does not escalate when DescribeFigures off", func(t *testing.T) {
		vlm := &stubChatter{reply: "[FIGURE: small embedded diagram]"}
		o := &OCR{Client: vlm, Cheap: stubEngine{"tesseract", cleanPO, nil}, DescribeFigures: false}
		_, engine, _, err := o.PageAsSeen(t.Context(), PageImage{
			Page: 1, Mime: "image/png", Data: pages[0].Data, HasEmbeddedImage: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if engine != "tesseract" {
			t.Errorf("engine = %q, want tesseract (flag off)", engine)
		}
		if vlm.called {
			t.Error("VLM was called despite DescribeFigures off")
		}
	})

	t.Run("no Xobject flag does not escalate even with DescribeFigures on", func(t *testing.T) {
		vlm := &stubChatter{reply: "[FIGURE: small embedded diagram]"}
		o := &OCR{Client: vlm, Cheap: stubEngine{"tesseract", cleanPO, nil}, DescribeFigures: true}
		_, engine, _, err := o.PageAsSeen(t.Context(), PageImage{
			Page: 1, Mime: "image/png", Data: pages[0].Data, HasEmbeddedImage: false,
		})
		if err != nil {
			t.Fatal(err)
		}
		if engine != "tesseract" {
			t.Errorf("engine = %q, want tesseract (no XObject flag)", engine)
		}
		if vlm.called {
			t.Error("VLM was called despite no XObject flag")
		}
	})

	// Threading: pdfUnits sets hasEmbeddedImage on the ingestUnit, and
	// unitsToPageText copies it into the PageImage it hands to the cascade.
	// Exercised via the Pagify path (no poppler required): every unit
	// Pagify extracts is by definition an image page, so the flag is set.
	t.Run("pdfUnits sets hasEmbeddedImage and unitsToPageText threads it", func(t *testing.T) {
		if HavePoppler() {
			t.Skip("poppler available — the Pagify path is not taken; threading is covered by the direct PageAsSeen cases above")
		}
		units, err := pdfUnits(t.Context(), pdfPath, true, stubEngine{"tesseract", cleanPO, nil}, RenderPolicy{})
		if err != nil {
			t.Fatal(err)
		}
		if len(units) != 1 {
			t.Fatalf("want 1 unit, got %d", len(units))
		}
		if !units[0].hasEmbeddedImage {
			t.Fatal("pdfUnits did not set hasEmbeddedImage on the extracted unit")
		}
		vlm := &stubChatter{reply: "[FIGURE: small embedded diagram]"}
		o := &OCR{Client: vlm, Cheap: stubEngine{"tesseract", cleanPO, nil}, DescribeFigures: true}
		pageTexts, err := unitsToPageText(t.Context(), units, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(pageTexts) != 1 {
			t.Fatalf("want 1 page text, got %d", len(pageTexts))
		}
		if pageTexts[0].Engine != "vision" {
			t.Errorf("engine = %q, want vision (vlm called %v)", pageTexts[0].Engine, vlm.called)
		}
		if !vlm.called {
			t.Error("VLM was not called — the XObject flag was not threaded through unitsToPageText")
		}
	})
}
