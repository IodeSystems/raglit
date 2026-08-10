package raglit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// perPageChatter fails the Nth OCR call and reads every other one. The OCR
// cascade calls once per image unit, in order, so "call 2" is "page 2".
type perPageChatter struct {
	failOn map[int]bool // 1-based call number
	calls  int
	text   string
}

func (c *perPageChatter) ChatStream(_ context.Context, _ []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.calls++
	if c.failOn[c.calls] {
		return nil, fmt.Errorf("the model repeated a 54-character block 10 times")
	}
	return streamReply(fmt.Sprintf("%s page %d", c.text, c.calls)), nil
}

func imgUnits(n int) []ingestUnit {
	u := make([]ingestUnit, 0, n)
	for i := 1; i <= n; i++ {
		u = append(u, ingestUnit{page: i, mime: "image/png", data: []byte(fmt.Sprintf("img-%d", i))})
	}
	return u
}

// THE REGRESSION. A 5-page document whose page 3 loops must still index pages
// 1,2,4,5. Measured on the delano corpus 2026-08-09: a 5-page lot certification
// and a 9-page billing narrative were indexed as NOTHING — no row, no fragment —
// because one page each tripped the repetition guard on a dense table. The
// extract path was fixed for exactly this on 2026-08-06 and the ingest path,
// which is what the daemon runs, never got it.
func TestOnePageFailingDoesNotDiscardTheDocument(t *testing.T) {
	s := openMem(t)
	ocr := NewOCR(&perPageChatter{failOn: map[int]bool{3: true}, text: "readable"})
	n, _, err := s.ingestUnits(context.Background(), nil, ocr, "cert.pdf", "Cert",
		imgUnits(5), FragConfig{}, nil)
	if err != nil {
		t.Fatalf("one bad page must not fail the document: %v", err)
	}
	if n == 0 {
		t.Fatal("no fragments committed — the document was discarded")
	}
	// The pages that read are searchable...
	for _, want := range []string{"page 1", "page 2", "page 4", "page 5"} {
		if h, _ := s.Search(want, 5); len(h) == 0 {
			t.Errorf("%q did not survive the salvage", want)
		}
	}
}

// A hole must NAME ITSELF. A partial document that looks complete is worse than
// a failed one: nothing ever goes looking for the missing page.
func TestTheUnreadPageIsRecordedAsAHole(t *testing.T) {
	s := openMem(t)
	ocr := NewOCR(&perPageChatter{failOn: map[int]bool{2: true}, text: "fine"})
	if _, _, err := s.ingestUnits(context.Background(), nil, ocr, "doc.pdf", "Doc",
		imgUnits(3), FragConfig{}, nil); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var engine string
	err := s.db.QueryRow(`SELECT engine FROM ocr_pages op JOIN documents d ON d.id=op.doc_id
	                       WHERE d.path='doc.pdf' AND op.page=2`).Scan(&engine)
	if err != nil {
		t.Fatalf("the failed page left no provenance row at all: %v", err)
	}
	if engine != "failed" {
		t.Errorf("page 2 is a hole; provenance says engine=%q", engine)
	}
	// And the pages that DID read are not marked failed.
	var ok int
	s.db.QueryRow(`SELECT COUNT(*) FROM ocr_pages op JOIN documents d ON d.id=op.doc_id
	                WHERE d.path='doc.pdf' AND op.engine='failed'`).Scan(&ok)
	if ok != 1 {
		t.Errorf("exactly one page failed; %d rows marked failed", ok)
	}
}

// Salvage must not become "commit anything". A document where NOTHING read is a
// failure, and must stay one — otherwise an unreadable scan enters the index as
// an empty document that search will report as present and quote as silence.
func TestEveryPageFailingIsStillAFailure(t *testing.T) {
	s := openMem(t)
	ocr := NewOCR(&perPageChatter{failOn: map[int]bool{1: true, 2: true, 3: true}})
	_, _, err := s.ingestUnits(context.Background(), nil, ocr, "blank.pdf", "Blank",
		imgUnits(3), FragConfig{}, nil)
	if err == nil {
		t.Fatal("a document where no page read must fail the ingest")
	}
	if !strings.Contains(err.Error(), "every page failed") {
		t.Errorf("the error must say what happened, got: %v", err)
	}
	var docs int
	s.db.QueryRow(`SELECT COUNT(*) FROM documents WHERE path='blank.pdf'`).Scan(&docs)
	if docs != 0 {
		t.Errorf("a wholly unread document must not be committed, got %d row(s)", docs)
	}
}

// The stage log is where an operator learns a document is incomplete. Silence
// here means the salvage hides the very thing it was built to surface.
func TestSalvageIsReportedOnTheStageLog(t *testing.T) {
	s := openMem(t)
	id, _ := s.Enqueue("d.pdf", "")
	sl := s.NewStageLog(id)
	ocr := NewOCR(&perPageChatter{failOn: map[int]bool{2: true}, text: "fine"})
	if _, _, err := s.ingestUnits(context.Background(), nil, ocr, "d.pdf", "D",
		imgUnits(4), FragConfig{}, sl); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	rows, err := s.db.Query(`SELECT detail FROM job_stages WHERE job_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var dump strings.Builder
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil {
			dump.WriteString(d + "\n")
		}
	}
	if !strings.Contains(dump.String(), "SALVAGED") {
		t.Errorf("an incomplete document must say so on the stage log:\n%s", dump.String())
	}
	if !strings.Contains(dump.String(), "p2") {
		t.Errorf("the stage line must name WHICH page is missing:\n%s", dump.String())
	}
}

// A text-layer page carries the document when an image page fails, and vice
// versa — the threshold is "did anything read", not "did any IMAGE read".
func TestATextPageAloneKeepsTheDocument(t *testing.T) {
	s := openMem(t)
	ocr := NewOCR(&perPageChatter{failOn: map[int]bool{1: true}})
	units := []ingestUnit{
		{page: 1, mime: "image/png", data: []byte("scan")},
		{page: 2, text: "born digital text that reads perfectly well"},
	}
	n, _, err := s.ingestUnits(context.Background(), nil, ocr, "mixed.pdf", "Mixed", units, FragConfig{}, nil)
	if err != nil {
		t.Fatalf("the text page should carry the document: %v", err)
	}
	if n == 0 {
		t.Fatal("nothing committed despite a readable text-layer page")
	}
	if h, _ := s.Search("born digital", 5); len(h) == 0 {
		t.Error("the readable page is not searchable")
	}
}
