package raglit

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// figurePNG encodes a grayscale page image. The three synthetic pages below
// are the three shapes the gate has to separate: a large dark block (a
// diagram), regular text lines, and blank paper.
func figurePNG(t *testing.T, m *image.Gray) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func grayPage(w, h int) *image.Gray {
	m := image.NewGray(image.Rect(0, 0, w, h))
	// NewGray is all Y=0 (black); a scanned page starts white.
	fillRect(m, 0, 0, w, h, color.Gray{Y: 255})
	return m
}

func fillRect(m *image.Gray, x0, y0, x1, y1 int, c color.Gray) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			m.SetGray(x, y, c)
		}
	}
}

// A 640x800 white page with a large dark block (a diagram) in the upper half
// and a few text lines below it — the shape of a scanned figure page: a
// concentrated block of ink on paper, roughly 30-35% total ink.
func figureImage(t *testing.T) []byte {
	m := grayPage(640, 800)
	fillRect(m, 80, 80, 560, 400, color.Gray{Y: 30}) // ~30% of the page
	for i := 0; i < 6; i++ { // text lines under the figure
		y := 480 + i*30
		fillRect(m, 60, y, 580, y+3, color.Gray{Y: 40})
	}
	return figurePNG(t, m)
}

// A 640x800 white page with ~20 thin dark lines of text, each 3px tall —
// regular text rhythm, low total ink.
func textImage(t *testing.T) []byte {
	m := grayPage(640, 800)
	for i := 0; i < 24; i++ {
		y := 60 + i*30
		fillRect(m, 60, y, 580, y+3, color.Gray{Y: 40})
	}
	return figurePNG(t, m)
}

// A completely blank white page.
func blankImage(t *testing.T) []byte {
	return figurePNG(t, grayPage(640, 800))
}

func TestFigureLikeImage(t *testing.T) {
	cases := []struct {
		name  string
		data  func(t *testing.T) []byte
		want  bool
	}{
		{"blank page is not figure-like", blankImage, false},
		{"regular text lines are not figure-like", textImage, false},
		{"a large dark block is figure-like", figureImage, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ink, block := figureLikeImage(c.data(t))
			if got != c.want {
				t.Fatalf("figureLikeImage = %v (ink %.2f, block %d rows), want %v", got, ink, block, c.want)
			}
		})
	}
	// Undecodable bytes are not figure-like — the cascade must behave as today.
	if got, _, _ := figureLikeImage([]byte("not an image")); got {
		t.Error("undecodable bytes must not be figure-like")
	}
}

// The cascade half: the gate only fires when the flag is on AND the read is
// clean AND the image is figure-like.
func TestFigureGate_Cascade(t *testing.T) {
	cleanPO := PageOCR{Text: "The quarterly report shows revenue grew twelve percent.", MeanConfidence: 0.97, BoxCount: 6}

	cases := []struct {
		name          string
		img           []byte
		describeOn    bool
		wantEngine    string
		wantVLMPrompt string // the prompt the VLM must have seen, when called
	}{
		{
			name:       "figure-like page escalates when flag on",
			img:        figureImage(t),
			describeOn: true,
			wantEngine: "vision",
		},
		{
			name:       "same figure-like page is cheap when flag off",
			img:        figureImage(t),
			describeOn: false,
			wantEngine: "tesseract",
		},
		{
			name:       "text-only page is cheap even when flag on",
			img:        textImage(t),
			describeOn: true,
			wantEngine: "tesseract",
		},
		{
			name:       "blank page is cheap even when flag on",
			img:        blankImage(t),
			describeOn: true,
			wantEngine: "tesseract",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vlm := &stubChatter{reply: "[FIGURE: bar chart of quarterly revenue.]"}
			o := &OCR{Client: vlm, Cheap: stubEngine{"tesseract", cleanPO, nil}, DescribeFigures: c.describeOn}
			text, engine, err := o.PageWithEngine(t.Context(), PageImage{Page: 1, Mime: "image/png", Data: c.img})
			if err != nil {
				t.Fatal(err)
			}
			if engine != c.wantEngine {
				t.Errorf("engine = %q, want %q (vlm called %v)", engine, c.wantEngine, vlm.called)
			}
			if engine == "vision" {
				// The description must come from the VLM read, and the prompt
				// must have carried the figure instruction.
				if !vlm.sawText {
					t.Error("VLM called without a text instruction")
				}
				if text != "[FIGURE: bar chart of quarterly revenue.]" {
					t.Errorf("text = %q, want the VLM's figure description", text)
				}
			} else if vlm.called {
				t.Errorf("cheap engine won but the VLM was called")
			}
		})
	}
}
