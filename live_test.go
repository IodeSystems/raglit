package raglit

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/agentkit/llm"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// TestLive_PDFOCRPipeline drives the real thing end-to-end: render text to an
// image, wrap it in a PDF, OCR each page through a vision model, index, and
// search. Guarded — runs only with RAGLIT_LIVE=1 (needs RAGLIT_LLM_KEY and a
// reachable vision endpoint; defaults to bonsai / gemma-4-12b).
//
//	RAGLIT_LIVE=1 RAGLIT_LLM_KEY=… go test -run TestLive ./...
func TestLive_PDFOCRPipeline(t *testing.T) {
	if os.Getenv("RAGLIT_LIVE") == "" {
		t.Skip("set RAGLIT_LIVE=1, RAGLIT_LLM_URL and RAGLIT_LLM_KEY to run the live OCR pipeline")
	}
	dir := t.TempDir()
	const phrase = "Refresh token rotates on each use and is single-use."
	pdfPath := renderTextPDF(t, dir, phrase)

	// NO BAKED DEFAULT. A library that ships somebody's private endpoint as its
	// fallback publishes that endpoint to everyone who reads it, and quietly
	// points a stranger's test run at it.
	base := os.Getenv("RAGLIT_LLM_URL")
	if base == "" {
		t.Skip("set RAGLIT_LLM_URL to the model gateway to run this")
	}
	modelID := envOr("RAGLIT_LLM_MODEL", "gemma-4-12b")
	client := llm.NewClient(base, os.Getenv("RAGLIT_LLM_KEY"), modelID)

	s, err := OpenHome(Home(filepath.Join(dir, "home")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	n, err := s.IngestPDF(ctx, NewOCR(client), pdfPath)
	if err != nil {
		t.Fatalf("IngestPDF: %v", err)
	}
	if n == 0 {
		t.Fatal("OCR produced no indexable text")
	}

	hits, err := s.Search("refresh token", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatalf("OCR'd text not searchable (model transcribed something unexpected)")
	}
	t.Logf("OCR→index→search OK: page %d text = %q", hits[0].Page, hits[0].Text)
}

// TestLive_SegmentCode checks the segmenter against the real small model: does
// it return schema-valid fragments for a code window (via the fix-loop), and is
// the content preserved? Guarded by RAGLIT_LIVE.
func TestLive_SegmentCode(t *testing.T) {
	if os.Getenv("RAGLIT_LIVE") == "" {
		t.Skip("set RAGLIT_LIVE=1 (and RAGLIT_LLM_KEY) to run live segmentation")
	}
	// NO BAKED DEFAULT. A library that ships somebody's private endpoint as its
	// fallback publishes that endpoint to everyone who reads it, and quietly
	// points a stranger's test run at it.
	base := os.Getenv("RAGLIT_LLM_URL")
	if base == "" {
		t.Skip("set RAGLIT_LLM_URL to the model gateway to run this")
	}
	model := envOr("RAGLIT_LLM_MODEL", "ternary-bonsai-27b")
	client := llm.NewClient(base, os.Getenv("RAGLIT_LLM_KEY"), model)

	const code = `package auth

// newAccessToken mints a short-lived access token for the session.
func newAccessToken(s *Session) (string, error) {
	return sign(s.ID, 15*time.Minute)
}

// rotateRefresh issues a fresh refresh token and invalidates the old one.
func rotateRefresh(s *Session) (string, error) {
	s.RefreshGen++
	return sign(s.ID, 30*24*time.Hour)
}`

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r, err := NewSegmenter(client).SegmentText(ctx, code, "")
	if err != nil {
		t.Fatalf("segment: %v", err)
	}
	if len(r.Fragments) == 0 {
		t.Fatal("no fragments")
	}
	joined := ""
	for i, f := range r.Fragments {
		t.Logf("fragment %d (%d chars): %.80s…", i, len(f.Text), f.Text)
		joined += f.Text
	}
	// Content preserved (not lost to a bad parse/fallback dropping text).
	if !strings.Contains(joined, "rotateRefresh") || !strings.Contains(joined, "newAccessToken") {
		t.Errorf("segmentation dropped code content: %q", joined)
	}
}

// renderTextPDF draws phrase onto a white PNG (real TTF, large enough for OCR)
// and wraps it in a one-page image-PDF.
func renderTextPDF(t *testing.T, dir, phrase string) string {
	t.Helper()
	ttf, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: 40, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1100, 160))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.Black), Face: face, Dot: fixed.P(30, 95)}
	d.DrawString(phrase)

	pngPath := filepath.Join(dir, "page.png")
	f, err := os.Create(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := api.ImportImagesFile(context.Background(), []string{pngPath}, pdfPath, pdfcpu.DefaultImportConfig(), model.NewDefaultConfiguration()); err != nil {
		t.Fatal(err)
	}
	return pdfPath
}

// TestLive_WorkOrderDocType is the live half of the schemaed-documents story:
// a real model proposes a doc_type from a gold document, identity resolves a
// document to that type, and the extraction comes back with the VALUES the
// document states — not merely well-shaped ones. The fixtures under testdrive
// cover the plumbing with a canned chatter; this measures the behaviour the
// plumbing exists for.
//
// Guarded on RAGLIT_LIVE, so CI (and the default suite) is unaffected: a
// model-dependent test is a measuring tool, and its value is in what it
// prints, not in gating.
//
//	RAGLIT_LIVE=1 RAGLIT_LLM_URL=… RAGLIT_LLM_KEY=… go test -run TestLive ./...
func TestLive_WorkOrderDocType(t *testing.T) {
	if os.Getenv("RAGLIT_LIVE") == "" {
		t.Skip("set RAGLIT_LIVE=1, RAGLIT_LLM_URL and RAGLIT_LLM_KEY to run the live doc-type test")
	}
	// NO BAKED DEFAULT. A library that ships somebody's private endpoint as its
	// fallback publishes that endpoint to everyone who reads it, and quietly
	// points a stranger's test run at it.
	base := os.Getenv("RAGLIT_LLM_URL")
	if base == "" {
		t.Skip("set RAGLIT_LLM_URL to the model gateway to run this")
	}
	modelID := envOr("RAGLIT_LLM_MODEL", "gemma-4-12b")
	client := llm.NewClient(base, os.Getenv("RAGLIT_LLM_KEY"), modelID)

	raw, err := os.ReadFile("testdata/workorder.txt")
	if err != nil {
		t.Fatal(err)
	}
	const docPath = "/corpus/workorder.txt"

	dir := t.TempDir()
	s, err := OpenHome(Home(filepath.Join(dir, "home")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ingest(context.Background(), Document{
		Path:      docPath,
		Title:     "workorder.txt",
		Fragments: []Fragment{{Page: 0, Ord: 0, Text: string(raw)}},
	}); err != nil {
		t.Fatal(err)
	}
	s.SetIdentifier(NewIdentifier(client, modelID))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// 1. Propose: a real model reads the gold document and proposes the type.
	// The proposal is returned, not stored — registration is the author's act.
	proposal, err := s.ProposeDocType(ctx, "Work Order", []string{docPath})
	if err != nil {
		t.Fatalf("ProposeDocType: %v", err)
	}
	if proposal.Name != "work order" {
		t.Errorf("proposal name = %q, want the normalised name", proposal.Name)
	}
	if strings.TrimSpace(proposal.Description) == "" {
		t.Error("the proposal carries no description — identity would have nothing to recognise the type by")
	}
	if err := ValidateFieldSchema(proposal.Schema); err != nil {
		t.Fatalf("the proposed schema is not a usable field schema: %v", err)
	}
	if n := len(proposal.FieldNames()); n == 0 {
		t.Error("the proposal declares no fields")
	}
	t.Logf("proposed type %q: %s", proposal.Name, proposal.Description)
	t.Logf("schema fields: %v", proposal.FieldNames())

	if err := s.SetDocType(proposal); err != nil {
		t.Fatalf("SetDocType: %v", err)
	}

	// 2. Resolve: the identity pass asks the model what the document is, with
	// the registered type in its vocabulary. This is where a document that IS
	// a registered type must resolve to it, so an extraction can chain.
	id, err := s.IdentifyDocument(ctx, docPath, false)
	if err != nil {
		t.Fatalf("IdentifyDocument: %v", err)
	}
	if id.DocType != "work order" {
		// One more ask, forced: the first pass can miss a recognition and the
		// second is the fair measurement of "can this model resolve it at all".
		id, err = s.IdentifyDocument(ctx, docPath, true)
		if err != nil {
			t.Fatalf("IdentifyDocument (forced): %v", err)
		}
	}
	t.Logf("caption: %s — doc_type: %q", id.Name, id.DocType)
	if id.DocType != "work order" {
		t.Fatalf("identity did not resolve the work order to its registered type (doc_type = %q); extraction cannot chain from it", id.DocType)
	}

	// 3. Extract: the F3 promise — the values, not the shape.
	got, err := s.ExtractFields(ctx, docPath, false)
	if err != nil {
		t.Fatalf("ExtractFields: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(got.Fields, &fields); err != nil {
		t.Fatalf("extracted fields are not an object: %v", err)
	}
	t.Logf("extracted: %s", string(got.Fields))

	// The three values the fixture states. The exact ones are the strong
	// assertion; on model drift these are the measurements to read, and the
	// strongest assertion that stays stable is the right one to keep.
	if v := strings.TrimSpace(asString(fields["order_number"])); v != "RO-04471" {
		t.Errorf("order_number = %q, want \"RO-04471\" (copied exactly as printed)", v)
	}
	if v := strings.TrimSpace(asString(fields["customer"])); v != "Doreen Voss" {
		t.Errorf("customer = %q, want \"Doreen Voss\"", v)
	}
	total, ok := asNumber(fields["total"])
	if !ok {
		t.Errorf("total = %v, want a number near 318.40", fields["total"])
	} else if math.Abs(total-318.40) > 0.01 {
		t.Errorf("total = %v, want 318.40", total)
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
