package raglit

import (
	"context"
	"fmt"
	"time"

	gen "github.com/iodesystems/raglit/internal/db"
)

// Segmented ingestion pipeline.
//
// A document is a sequence of UNITS (page images for a PDF; text windows for a
// file). Each unit is segmented by the LLM (segment.go), the Assembler stitches
// fragments across unit boundaries (deferring the open fragment), and finalized
// fragments are embedded by a CONCURRENT pipeline while segmentation works the
// next unit.
//
// ATOMICITY: the whole new document is built in memory first (fragments +
// vectors + page provenance), then swapped in under ONE transaction at the very
// end. Nothing touches the index until every unit has succeeded — so a mid-ingest
// failure (e.g. the LLM is unreachable) leaves the PRIOR indexed version intact
// rather than a torn, half-updated document. (The earlier design cleared the doc
// up front and inserted per-unit, so a failure destroyed the good copy and left
// partial fragments behind.)

// ingestUnit is one segmentation unit: an image (mime+data) or a text window.
type ingestUnit struct {
	page int
	mime string
	data []byte // set → image unit
	text string // set → text unit
}

func (u ingestUnit) isImage() bool { return len(u.data) > 0 }

// stagedFrag is a finalized fragment held in memory until the atomic swap.
type stagedFrag struct {
	page, ord int
	text      string
}

// stagedPage is a page's provenance held in memory until the atomic swap.
type stagedPage struct {
	page    int
	engine  string
	imgPath string
}

// ingestUnits runs the per-unit pipeline and commits the result atomically.
// STAGES (recorded via sl, which may be nil): an IMAGE unit is first OCR'd to
// text by the cascade (cheap→gate→VLM, ocr required) — that's the "ocr" task,
// tagged with the real engine per page — and THEN segmented as text; a TEXT unit
// (born-digital PDF page / text window) skips ocr and is segmented directly.
// Segmentation ("segment", engine "llm") turns page text into fragments,
// embedding ("embed") runs concurrently, and everything is swapped in under one
// transaction ("commit"). Returns the number of fragments indexed. ocr may be
// nil when no unit is an image.
func (s *Store) ingestUnits(ctx context.Context, sg *Segmenter, ocr *OCR, docPath, title string, units []ingestUnit, sl *StageLog) (int, error) {
	var frags []stagedFrag
	var provenance []stagedPage
	ocrEngines := map[string]int{}

	// Concurrent embed pipeline (only when a store has an embedder). It embeds
	// finalized fragments while later units segment, but holds the vectors in
	// memory (keyed by fragment index) — they're written in the final swap, not
	// as they're produced.
	var embedCh chan embedItem
	var embedDone chan embedResult
	if s.embedder != nil {
		embedCh = make(chan embedItem, 64)
		embedDone = make(chan embedResult, 1)
		go runStagedEmbed(ctx, s.embedder, embedCh, embedDone)
	}

	a := NewAssembler(func(page, ord int, text string) error {
		idx := len(frags)
		frags = append(frags, stagedFrag{page: page, ord: ord, text: text})
		if embedCh != nil {
			embedCh <- embedItem{idx: idx, text: text}
		}
		return nil
	})

	// failPhase drains the embed goroutine (so it exits) and records the stage
	// where ingestion failed, returning the error unchanged.
	failPhase := func(phase, engine string, err error) error {
		if embedCh != nil {
			close(embedCh)
			<-embedDone
			embedCh = nil
		}
		sl.Fail(phase, engine, err)
		return err
	}

	segErr := func() error {
		for _, u := range units {
			open := a.OpenText()
			text := u.text
			// Stage 1 (image units only): OCR the page image to text.
			if u.isImage() {
				if ocr == nil {
					return failPhase("ocr", "", fmt.Errorf("page %d is an image but no OCR is configured", u.page))
				}
				t, engine, err := ocr.PageWithEngine(ctx, PageImage{Page: u.page, Mime: u.mime, Data: u.data})
				if err != nil {
					return failPhase("ocr", engine, err)
				}
				text = t
				ocrEngines[engine]++
				// Save the page image + record provenance with the REAL cascade engine
				// (tesseract/paddleocr/vision), so review shows which pages escalated.
				// The image write is idempotent (deterministic path); the ocr_pages row
				// is written in the atomic swap.
				imgPath := ""
				if p, e := s.savePageImage(docPath, u.page, u.mime, u.data); e == nil {
					imgPath = p
				}
				provenance = append(provenance, stagedPage{page: u.page, engine: engine, imgPath: imgPath})
			} else if u.page >= 1 {
				// A born-digital / text-layer page: no OCR, engine "text".
				provenance = append(provenance, stagedPage{page: u.page, engine: "text"})
			}
			// Stage 2: segment the (possibly OCR'd) page text into fragments.
			r, err := sg.SegmentText(ctx, text, open)
			if err != nil {
				return failPhase("segment", "llm", err)
			}
			if err := a.Feed(u.page, r); err != nil {
				return failPhase("segment", "llm", err)
			}
		}
		if err := a.Close(); err != nil {
			return failPhase("segment", "llm", err)
		}
		return nil
	}()
	if segErr != nil {
		return 0, segErr // prior version untouched; nothing was written
	}

	// Record the ocr + segment tasks now that they've completed.
	if len(ocrEngines) > 0 {
		n := 0
		for _, c := range ocrEngines {
			n += c
		}
		sl.Done("ocr", engineSummary(ocrEngines), fmt.Sprintf("%d page(s)", n))
	}
	sl.Done("segment", "llm", fmt.Sprintf("%d fragment(s)", len(frags)))

	// Drain the embed pipeline before committing.
	var vecs map[int][]float32
	if embedCh != nil {
		close(embedCh)
		res := <-embedDone
		if res.err != nil {
			sl.Fail("embed", "", res.err)
			return 0, res.err
		}
		vecs = res.vecs
		sl.Done("embed", "vectors", fmt.Sprintf("%d vector(s)", len(vecs)))
	}

	if err := s.commitDoc(docPath, title, frags, provenance, vecs); err != nil {
		sl.Fail("commit", "", err)
		return 0, err
	}
	sl.Done("commit", "", "")
	return len(frags), nil
}

// commitDoc swaps a freshly-built document into the index in ONE transaction:
// upsert the document, drop its prior fragments/vectors/provenance (FK cascade
// drops vectors; FTS triggers clean the mirror), then insert the new fragments
// (capturing their ids for vectors), vectors, and page provenance. All-or-nothing
// — search never observes a half-updated document.
func (s *Store) commitDoc(docPath, title string, frags []stagedFrag, provenance []stagedPage, vecs map[int][]float32) error {
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	q := gq(tx) // generated queries bound to this tx

	docID, err := q.UpsertDocument(ctx, gen.UpsertDocumentParams{Path: docPath, Title: title, AddedAt: time.Now().UnixNano()})
	if err != nil {
		return fmt.Errorf("raglit: commit doc: %w", err)
	}
	if err := q.DeleteFragmentsByDoc(ctx, docID); err != nil {
		return err
	}
	if err := q.DeleteOcrPagesByDoc(ctx, docID); err != nil {
		return err
	}
	for i, f := range frags {
		fid, err := q.InsertFragment(ctx, gen.InsertFragmentParams{DocID: docID, Page: int64(f.page), Ord: int64(f.ord), Text: f.text})
		if err != nil {
			return fmt.Errorf("raglit: insert fragment: %w", err)
		}
		if v := vecs[i]; len(v) > 0 {
			if err := q.InsertVector(ctx, gen.InsertVectorParams{FragmentID: fid, Dim: int64(len(v)), Vec: encodeVec(v)}); err != nil {
				return fmt.Errorf("raglit: store vector: %w", err)
			}
		}
	}
	for _, p := range provenance {
		if err := q.UpsertOcrPage(ctx, gen.UpsertOcrPageParams{DocID: docID, Page: int64(p.page), Engine: p.engine, ImagePath: p.imgPath}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// embedItem is a finalized fragment (by its index in the staged slice) awaiting
// embedding; embedResult is the concurrent pipeline's collected vectors.
type embedItem struct {
	idx  int
	text string
}
type embedResult struct {
	vecs map[int][]float32 // fragment index → vector
	err  error
}

// runStagedEmbed batches finalized fragments and embeds them, collecting the
// vectors in memory keyed by fragment index (they're written later, in the
// atomic swap). It always drains ch so a producer never blocks, and reports the
// first error. The returned map is only safe to read after receiving from done
// (channel receive establishes the happens-before).
func runStagedEmbed(ctx context.Context, emb *Embedder, ch <-chan embedItem, done chan<- embedResult) {
	const batch = 16
	vecs := map[int][]float32{}
	var buf []embedItem
	var firstErr error

	flush := func() {
		if len(buf) == 0 || firstErr != nil {
			buf = nil
			return
		}
		texts := make([]string, len(buf))
		for i, it := range buf {
			texts[i] = it.text
		}
		out, err := emb.EmbedDocs(ctx, texts)
		if err != nil {
			firstErr = err
			buf = nil
			return
		}
		for i, it := range buf {
			if i >= len(out) {
				break
			}
			vecs[it.idx] = out[i]
		}
		buf = nil
	}

	for it := range ch {
		buf = append(buf, it)
		if len(buf) >= batch {
			flush()
		}
	}
	flush()
	done <- embedResult{vecs: vecs, err: firstErr}
}
