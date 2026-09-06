package raglit

import (
	"context"
	"errors"
	"testing"
)

// stubEngine is a PageEngine returning a canned PageOCR (or error), for the
// cascade tests — no real tesseract/paddle needed.
type stubEngine struct {
	name string
	po   PageOCR
	err  error
}

func (s stubEngine) Name() string { return s.name }
func (s stubEngine) OCRPage(context.Context, PageImage) (PageOCR, error) {
	return s.po, s.err
}

func TestCascade(t *testing.T) {
	cleanPO := PageOCR{Text: "The quarterly report shows revenue grew twelve percent over the period.", MeanConfidence: 0.97, BoxCount: 6}
	garblePO := PageOCR{Text: "brqwx ttttt zxcvb knmpq wrtgh sdfgh jklmn bcdfg pqrst vwxyz", MeanConfidence: 0.92, BoxCount: 10}
	emptyPO := PageOCR{Text: "", MeanConfidence: 0, BoxCount: 0}

	cases := []struct {
		name       string
		cheap      PageEngine
		wantEngine string
		wantVLM    bool // did the VLM get called?
		wantText   string
	}{
		{"clean cheap pass is trusted", stubEngine{"tesseract", cleanPO, nil}, "tesseract", false, cleanPO.Text},
		{"gibberish escalates to vlm", stubEngine{"tesseract", garblePO, nil}, "vision", true, "VLM TEXT"},
		{"empty page not escalated", stubEngine{"tesseract", emptyPO, nil}, "tesseract", false, ""},
		{"cheap error degrades to vlm", stubEngine{"tesseract", PageOCR{}, errors.New("exec: not found")}, "vision", true, "VLM TEXT"},
		{"no cheap engine is vlm-only", nil, "vision", true, "VLM TEXT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vlm := &stubChatter{reply: "  VLM TEXT  "}
			o := &OCR{Client: vlm, Cheap: c.cheap}
			text, engine, err := o.PageWithEngine(context.Background(), PageImage{Page: 1, Mime: "image/png"})
			if err != nil {
				t.Fatal(err)
			}
			if engine != c.wantEngine {
				t.Errorf("engine = %q, want %q", engine, c.wantEngine)
			}
			if vlm.called != c.wantVLM {
				t.Errorf("vlm called = %v, want %v", vlm.called, c.wantVLM)
			}
			if text != c.wantText {
				t.Errorf("text = %q, want %q", text, c.wantText)
			}
		})
	}
}

func TestBuildPageEngine(t *testing.T) {
	if e, err := BuildPageEngine(OCRConfig{CheapEngine: ""}); e != nil || err != nil {
		t.Errorf(`"" → want (nil,nil), got (%v,%v)`, e, err)
	}
	if e, err := BuildPageEngine(OCRConfig{CheapEngine: "none"}); e != nil || err != nil {
		t.Errorf(`none → want (nil,nil), got (%v,%v)`, e, err)
	}
	if e, err := BuildPageEngine(OCRConfig{CheapEngine: "tesseract"}); err != nil || e == nil || e.Name() != "tesseract" {
		t.Errorf("tesseract → want engine, got (%v,%v)", e, err)
	}
	if e, err := BuildPageEngine(OCRConfig{CheapEngine: "paddleocr", PaddleURL: "http://x:7710"}); err != nil || e == nil || e.Name() != "paddleocr" {
		t.Errorf("paddleocr → want engine, got (%v,%v)", e, err)
	}
	if _, err := BuildPageEngine(OCRConfig{CheapEngine: "paddleocr"}); err == nil {
		t.Error("paddleocr without url → want error")
	}
	if _, err := BuildPageEngine(OCRConfig{CheapEngine: "bogus"}); err == nil {
		t.Error("unknown engine → want error")
	}
}

// TestParseTesseractTSV covers the TSV→PageOCR mapping without invoking the
// binary: line grouping (space within a line, newline across), confidence
// scaling (0..100 → 0..1), skipping conf<0 rows, and the empty-page (0 words) case.
func TestParseTesseractTSV(t *testing.T) {
	const header = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext"
	tsv := header + "\n" +
		"5\t1\t1\t1\t1\t1\t0\t0\t9\t9\t96.0\tHello\n" +
		"5\t1\t1\t1\t1\t2\t0\t0\t9\t9\t90.0\tworld\n" +
		"5\t1\t1\t1\t2\t1\t0\t0\t9\t9\t-1\t\n" + // no-recognition row, skipped
		"5\t1\t1\t1\t2\t2\t0\t0\t9\t9\t80.0\tnext\n"
	po := parseTesseractTSV(tsv)
	if po.Text != "Hello world\nnext" {
		t.Errorf("text = %q, want %q", po.Text, "Hello world\nnext")
	}
	if po.BoxCount != 3 {
		t.Errorf("boxCount = %d, want 3", po.BoxCount)
	}
	if got := po.MeanConfidence; got < 0.88 || got > 0.89 { // (96+90+80)/3/100 = 0.8867
		t.Errorf("meanConfidence = %v, want ~0.887", got)
	}
	if empty := parseTesseractTSV(header + "\n"); empty.BoxCount != 0 || empty.Text != "" {
		t.Errorf("empty tsv → want blank page, got %+v", empty)
	}
}
