package raglit

import (
	"context"
	"fmt"
	"time"
)

// Segmented ingestion pipeline.
//
// A document is a sequence of UNITS (page images for a PDF; text windows for a
// file). Each unit is segmented by the LLM (segment.go), the Assembler stitches
// fragments across unit boundaries (deferring the open fragment), and closed
// fragments are (a) inserted for BM25 immediately and (b) handed to a CONCURRENT
// embed pipeline that stores vectors while segmentation works the next unit.

// ingestUnit is one segmentation unit: an image (mime+data) or a text window.
type ingestUnit struct {
	page int
	mime string
	data []byte // set → image unit
	text string // set → text unit
}

func (u ingestUnit) isImage() bool { return len(u.data) > 0 }

// pendingFrag is a finalized fragment awaiting embedding.
type pendingFrag struct {
	id   int64
	text string
}

// ingestUnits runs the segment→assemble→(insert + concurrent embed) pipeline for
// one document. Returns the number of fragments indexed. Reingest replaces the
// document's prior fragments.
func (s *Store) ingestUnits(ctx context.Context, sg *Segmenter, docPath, title string, units []ingestUnit) (int, error) {
	docID, err := s.beginDoc(docPath, title)
	if err != nil {
		return 0, err
	}

	// Concurrent embed pipeline (only when a store has an embedder).
	var embedCh chan pendingFrag
	var embedDone chan error
	if s.embedder != nil {
		embedCh = make(chan pendingFrag, 64)
		embedDone = make(chan error, 1)
		go s.runEmbedPipeline(ctx, embedCh, embedDone)
	}

	count := 0
	a := NewAssembler(func(page, ord int, text string) error {
		id, err := s.insertFragment(docID, page, ord, text)
		if err != nil {
			return err
		}
		count++
		if embedCh != nil {
			embedCh <- pendingFrag{id: id, text: text}
		}
		return nil
	})

	segErr := func() error {
		for _, u := range units {
			open := a.OpenText()
			var r SegResult
			var err error
			if u.isImage() {
				r, err = sg.SegmentImage(ctx, u.mime, u.data, open)
			} else {
				r, err = sg.SegmentText(ctx, u.text, open)
			}
			if err != nil {
				return err
			}
			if err := a.Feed(u.page, r); err != nil {
				return err
			}
		}
		return a.Close()
	}()

	// Drain the embed pipeline before returning either error.
	if embedCh != nil {
		close(embedCh)
		if e := <-embedDone; e != nil && segErr == nil {
			segErr = e
		}
	}
	if segErr != nil {
		return 0, segErr
	}
	return count, nil
}

// runEmbedPipeline batches finalized fragments and stores their vectors. It
// always drains ch (so a producer never blocks) and reports the first error.
func (s *Store) runEmbedPipeline(ctx context.Context, ch <-chan pendingFrag, done chan<- error) {
	const batch = 16
	var buf []pendingFrag
	var firstErr error

	flush := func() {
		if len(buf) == 0 || firstErr != nil {
			buf = nil
			return
		}
		texts := make([]string, len(buf))
		for i, f := range buf {
			texts[i] = f.text
		}
		vecs, err := s.embedder.EmbedDocs(ctx, texts)
		if err != nil {
			firstErr = err
			buf = nil
			return
		}
		for i, f := range buf {
			if i >= len(vecs) {
				break
			}
			if err := s.storeVector(f.id, vecs[i]); err != nil {
				firstErr = err
				break
			}
		}
		buf = nil
	}

	for f := range ch {
		buf = append(buf, f)
		if len(buf) >= batch {
			flush()
		}
	}
	flush()
	done <- firstErr
}

// beginDoc upserts the document and clears its prior fragments (reingest is
// idempotent; FK cascade drops the old vectors).
func (s *Store) beginDoc(docPath, title string) (int64, error) {
	if _, err := s.db.Exec(
		`INSERT INTO documents(path, title, added_at) VALUES(?,?,?)
		 ON CONFLICT(path) DO UPDATE SET title=excluded.title, added_at=excluded.added_at`,
		docPath, title, time.Now().UnixNano()); err != nil {
		return 0, fmt.Errorf("raglit: begin doc: %w", err)
	}
	var docID int64
	if err := s.db.QueryRow(`SELECT id FROM documents WHERE path=?`, docPath).Scan(&docID); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`DELETE FROM fragments WHERE doc_id=?`, docID); err != nil {
		return 0, err
	}
	return docID, nil
}

func (s *Store) insertFragment(docID int64, page, ord int, text string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO fragments(doc_id, page, ord, text) VALUES(?,?,?,?)`, docID, page, ord, text)
	if err != nil {
		return 0, fmt.Errorf("raglit: insert fragment: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) storeVector(fragID int64, vec []float32) error {
	_, err := s.db.Exec(
		`INSERT INTO fragment_vectors(fragment_id, dim, vec) VALUES(?,?,?)`,
		fragID, len(vec), encodeVec(vec))
	return err
}
