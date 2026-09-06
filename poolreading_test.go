package raglit

import (
	"context"
	"path/filepath"
	"testing"
)

// What a POOLED reuse loses, and must not.
//
// The pool replays a document into another index without OCR, without a model,
// and without the layout markup — so anything the first read established has to
// travel in the payload or it is gone. Three things did not travel, and each one
// made the second index's copy claim something the first did not.

// pooledFixture ingests a screenshot-ish document into a fresh index and returns
// its exported pool payload.
func pooledFixture(t *testing.T, doc string) (*Store, PooledDoc) {
	t.Helper()
	s := testStore(t)
	sl := s.NewStageLog(0)
	units := []ingestUnit{{page: 1, text: screenshotPage}, {page: 2, text: screenshotPage}}
	if _, _, err := s.ingestUnits(context.Background(), nil, nil, doc, "sms.pdf", units, FragConfig{}, sl); err != nil {
		t.Fatal(err)
	}
	// As the pipeline records pages a model read, and a description it may not be
	// quoted from.
	if _, err := s.db.Exec(
		`UPDATE ocr_pages SET engine='vision' WHERE doc_id=(SELECT id FROM documents WHERE path=?)`, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE fragments SET origin=? WHERE doc_id=(SELECT id FROM documents WHERE path=?) AND ord=0`,
		FragOriginDescribed, doc); err != nil {
		t.Fatal(err)
	}
	pd, err := s.ExportDoc(doc)
	if err != nil {
		t.Fatal(err)
	}
	return s, pd
}

// A model's account of an image arrived in the next index as an ORDINARY
// fragment. origin='described' means findable but not quotable as the record,
// and it was dropped on export — so "a red Chevrolet Malibu, licence plate
// CEP0912" became indistinguishable from a sentence off a fax, in every index
// but the first.
func TestPooled_TheDescribedMarkSurvivesReuse(t *testing.T) {
	const doc = "file:///corpus/sms.pdf"
	_, pd := pooledFixture(t, doc)

	var marked int
	for _, f := range pd.Fragments {
		if f.Origin == FragOriginDescribed {
			marked++
		}
	}
	if marked == 0 {
		t.Fatal("the payload carries no described mark — reuse would replay a model's prose as the record")
	}

	// Replay into a second index, as a reuse does.
	dst := testStore(t)
	if _, err := dst.IngestPooled(context.Background(), doc, "sms.pdf", pd, ""); err != nil {
		t.Fatal(err)
	}
	var got int
	if err := dst.db.QueryRow(
		`SELECT COUNT(*) FROM fragments WHERE origin=? AND doc_id=(SELECT id FROM documents WHERE path=?)`,
		FragOriginDescribed, doc).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != marked {
		t.Fatalf("%d of %d described fragments survived the pool", got, marked)
	}
}

// And the per-page measurement, which reuse can never recompute: the markup it
// is taken from does not exist by then.
func TestPooled_TheMeasurementSurvivesReuse(t *testing.T) {
	const doc = "file:///corpus/sms.pdf"
	_, pd := pooledFixture(t, doc)
	for _, p := range pd.Pages {
		if p.TextChars == 0 {
			t.Fatalf("page %d pooled with no measurement — the second index cannot recover it", p.Page)
		}
	}
	dst := testStore(t)
	if _, err := dst.IngestPooled(context.Background(), doc, "sms.pdf", pd, ""); err != nil {
		t.Fatal(err)
	}
	_, pages, err := dst.DocReview(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, pg := range pages {
		if got := pg.DescribedPct(); got < 20 || got > 70 {
			t.Fatalf("reused page %d scored %d%% described", pg.Page, got)
		}
	}
}

// A pooled ingest records a reading, the same as a fresh one.
//
// It was the one path that did not, so a document's trust depended on which
// index happened to read it first: the first got vision-ocr with both facets,
// every index that reused it got no reading at all.
func TestPooled_ReuseRecordsAReading(t *testing.T) {
	const doc = "file:///corpus/sms.pdf"
	_, pd := pooledFixture(t, doc)

	dst := testStore(t)
	pool, err := OpenPool(filepath.Join(t.TempDir(), "pool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Put("recipe", "filehash", pd); err != nil {
		t.Fatal(err)
	}

	w := &Worker{Store: dst, Pool: pool, RecipeHash: "recipe"}
	if _, err := dst.IngestPooled(context.Background(), doc, "sms.pdf", pd, ""); err != nil {
		t.Fatal(err)
	}
	w.recordIngestReading(doc, "filehash", KindPDF, dst.NewStageLog(0))

	r, ok, err := dst.ReadingFor(doc)
	if err != nil || !ok {
		t.Fatalf("a reused document has no reading: ok=%v err=%v", ok, err)
	}
	if r.Method != MethodVision {
		t.Fatalf("method %q — the pooled pages were read by a model", r.Method)
	}
	if r.DescribedPct < 20 || r.DescribedPct > 70 {
		t.Fatalf("described %d%% — the measurement did not survive into the reading", r.DescribedPct)
	}
	if r.SourceSHA256 != "filehash" {
		t.Fatalf("source %q — a reading joins other accounts of the same bytes on this", r.SourceSHA256)
	}
}

// An OLD payload has no measurement, and the reading must say UNKNOWN rather
// than zero — 0% would assert "a model made none of this up" about exactly the
// documents most likely to be screenshots.
func TestPooled_AnUnmeasuredPayloadIsNotRecordedAsZero(t *testing.T) {
	const doc = "file:///corpus/old.pdf"
	_, pd := pooledFixture(t, doc)
	// As a payload pooled before the counts existed reads back.
	for i := range pd.Pages {
		pd.Pages[i].TextChars, pd.Pages[i].DescribedChars = 0, 0
	}
	dst := testStore(t)
	if _, err := dst.IngestPooled(context.Background(), doc, "old.pdf", pd, ""); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: dst}
	w.recordIngestReading(doc, "filehash", KindPDF, dst.NewStageLog(0))

	r, ok, err := dst.ReadingFor(doc)
	if err != nil || !ok {
		t.Fatalf("no reading: ok=%v err=%v", ok, err)
	}
	if r.DescribedPct != DescribedUnmeasured {
		t.Fatalf("described %d — an unmeasured document must not claim it described nothing", r.DescribedPct)
	}
	if r.Describes {
		t.Fatal("unmeasured is not described")
	}
	// Unknown means no claim either way, not a claim of zero.
	if _, ok := r.Trust()[FacetSubject]; ok {
		t.Fatal("an unmeasured reading asserted a subject claim it cannot support")
	}
	if r.Trust()[FacetText] != TrustVisionText {
		t.Fatal("the text claim still stands — a model did read these pages")
	}
}
