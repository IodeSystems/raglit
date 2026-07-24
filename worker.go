package raglit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sha256hex is the hex sha256 of raw bytes — the source-content fingerprint used
// for ingest dedup (skip re-indexing unchanged content).
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashHex is the hex sha256 of b — exported so callers can build pool recipe keys.
func HashHex(b []byte) string { return sha256hex(b) }

// Worker drains the ingest queue (queue.go): claim a pending job → fetch its
// URL → turn it into a Document (plain-text fragmenting, or PDF → OCR) → Ingest
// → mark done/error. Run it in the background of `serve`, or step it once from
// the CLI. A per-URL failure is recorded on the job, not fatal — one bad URL
// never stops the worker.
type Worker struct {
	Store *Store
	// OCR ingests PDF jobs. nil → a PDF job fails with a clear message (a text
	// corpus needs no vision model).
	OCR *OCR
	// Segmenter, if set, LLM-segments TEXT/code jobs (windowed to WindowChars)
	// instead of blank-line splitting — the "very good at code" path. nil → the
	// dependency-free blank-line fallback (fully offline). WindowChars comes from
	// config/default (see WindowCharsForHome); 0 → a safe default.
	Segmenter   *Segmenter
	WindowChars int
	// Fetcher overrides URL resolution (tests). nil → Fetch (file://, http(s)://).
	Fetcher func(ctx context.Context, url string) (Fetched, error)
	// IdlePoll is how long Run waits when the queue is empty. Default 500ms.
	IdlePoll time.Duration
	// Pool + RecipeHash enable cross-index dedup of INDEXING work: a fresh ingest
	// is cached in the pool keyed by (RecipeHash, source-file hash); a matching
	// key — from ANY index, or a retry — is reused (fragments + vectors + images
	// copied in) instead of re-running the LLM. RecipeHash captures the models +
	// config that shape the output, so alt models are a new key. nil Pool →
	// per-index dedup only (content_hash).
	Pool       *Pool
	RecipeHash string
}

func (w *Worker) fetch(ctx context.Context, url string) (Fetched, error) {
	if w.Fetcher != nil {
		return w.Fetcher(ctx, url)
	}
	return Fetch(ctx, url)
}

// ProcessOne claims and processes a single job. processed is false when the
// queue is empty. A fetch/ingest failure is recorded on the job and returns
// (true, nil); a returned error is infrastructure (db) failure only.
func (w *Worker) ProcessOne(ctx context.Context) (processed bool, err error) {
	job, err := w.Store.claimNextJob()
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	sl := w.Store.NewStageLog(job.ID)
	n, mode, ierr := w.ingest(ctx, job, sl)
	if ierr != nil {
		return true, w.Store.failJob(job.ID, ierr.Error())
	}
	return true, w.Store.completeJob(job.ID, n, mode)
}

// Drain processes jobs until the queue is empty, returning how many it handled.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		did, err := w.ProcessOne(ctx)
		if err != nil {
			return n, err
		}
		if !did {
			return n, nil
		}
		n++
	}
}

// Run drains the queue forever, sleeping IdlePoll between empty polls, until the
// context is canceled. This is the background loop `serve` starts.
func (w *Worker) Run(ctx context.Context) {
	poll := w.IdlePoll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	for {
		if ctx.Err() != nil {
			return
		}
		did, err := w.ProcessOne(ctx)
		if err != nil || !did {
			select {
			case <-ctx.Done():
				return
			case <-time.After(poll):
			}
		}
	}
}

// ingest turns a job's URL into indexed fragments, recording each pipeline stage
// via sl and returning (fragment count, segmentation mode, error). mode is "llm"
// when the LLM segmenter ran, "offline" for the dependency-free blank-line split.
func (w *Worker) ingest(ctx context.Context, job *Job, sl *StageLog) (int, string, error) {
	f, err := w.fetch(ctx, job.URL)
	if err != nil {
		sl.Fail("fetch", "", err)
		return 0, "", err
	}
	sl.Done("fetch", "", fmt.Sprintf("%d bytes", len(f.Data)))

	hash := sha256hex(f.Data)
	title := job.Title
	if title == "" {
		title = f.Title
	}

	// Fast path — same index, identical bytes: skip entirely (nothing to do).
	if prev, _ := w.Store.DocumentHash(job.URL); prev != "" && prev == hash {
		sl.Skip("extract", "unchanged — source hash match")
		return 0, "unchanged", nil
	}

	// Cross-index reuse — this (recipe, file) was processed anywhere before: copy
	// the cached fragments + vectors + page images in (cheap), no LLM.
	if w.Pool != nil && w.RecipeHash != "" {
		if doc, ok, _ := w.Pool.Get(w.RecipeHash, hash); ok {
			t := title
			if job.Title == "" && doc.Title != "" {
				t = doc.Title
			}
			if n, err := w.Store.IngestPooled(ctx, job.URL, t, doc, w.Pool.FileDir(hash)); err == nil {
				_ = w.Store.SetDocumentHash(job.URL, hash)
				sl.Skip("extract", "pooled — reused cached processing (recipe+file match)")
				return n, "pooled", nil
			}
			// copy failed → fall through and reprocess.
		}
	}

	// Process fresh, then remember it (per-index hash + shared pool).
	n, mode, ierr := w.extractAndIngest(ctx, job, f, title, sl)
	if ierr == nil {
		_ = w.Store.SetDocumentHash(job.URL, hash)
		if w.Pool != nil && w.RecipeHash != "" {
			if doc, e := w.Store.ExportDoc(job.URL); e == nil {
				_ = w.Pool.Put(w.RecipeHash, hash, doc)
			}
		}
	}
	return n, mode, ierr
}

// extractAndIngest routes a fetched document by kind (extract.go) and indexes it:
// a PDF runs the text-layer/OCR hybrid, an office/markup file goes through
// pandoc, an image through OCR, and anything else is treated as text.
func (w *Worker) extractAndIngest(ctx context.Context, job *Job, f Fetched, title string, sl *StageLog) (int, string, error) {
	kind := ClassifyDoc(job.URL, f.ContentType)
	if f.IsPDF {
		kind = KindPDF
	}
	switch kind {
	case KindPDF:
		if w.OCR == nil {
			err := fmt.Errorf("pdf ingest needs a vision model — configure one (raglit init) and serve with OCR")
			sl.Fail("extract", "pdf", err)
			return 0, "", err
		}
		path, cleanup, err := writeTemp(f.Data, ".pdf")
		if err != nil {
			return 0, "", err
		}
		defer cleanup()
		// ingestPDF records the extract + ocr + segment + embed + commit stages.
		n, err := w.Store.ingestPDF(ctx, w.OCR, job.URL, path, title, sl)
		return n, "llm", err

	case KindImage:
		if w.OCR == nil {
			err := fmt.Errorf("image ingest needs a vision model — configure one (raglit init) and serve with OCR")
			sl.Fail("extract", "image", err)
			return 0, "", err
		}
		sl.Done("extract", "image", "1 page")
		mime := mimeForExt(filepath.Ext(job.URL))
		units := []ingestUnit{{page: 1, mime: mime, data: f.Data}}
		// ingestUnits OCRs the image → text, then segments it (records ocr/segment/…).
		n, err := w.Store.ingestUnits(ctx, NewSegmenter(w.OCR.Client), w.OCR, job.URL, title, units, sl)
		return n, "llm", err

	case KindOffice:
		path, cleanup, err := writeTemp(f.Data, strings.ToLower(filepath.Ext(job.URL)))
		if err != nil {
			return 0, "", err
		}
		defer cleanup()
		text, err := PandocText(ctx, path)
		if err != nil {
			sl.Fail("extract", "pandoc", err)
			return 0, "", err
		}
		sl.Done("extract", "pandoc", fmt.Sprintf("%d chars", len(text)))
		return w.ingestPlainText(ctx, job.URL, title, []byte(text), sl)
	}

	// KindText / KindUnknown: read as text.
	sl.Done("extract", "text", fmt.Sprintf("%d bytes", len(f.Data)))
	return w.ingestPlainText(ctx, job.URL, title, f.Data, sl)
}

// ingestPlainText segments text with the LLM segmenter when configured (mode
// "llm"), else falls back to a blank-line paragraph split (mode "offline", fully
// offline — a code/text file → fragments directly, no OCR).
func (w *Worker) ingestPlainText(ctx context.Context, url, title string, data []byte, sl *StageLog) (int, string, error) {
	if w.Segmenter != nil {
		n, err := w.Store.ingestText(ctx, w.Segmenter, url, title, string(data), w.WindowChars, sl)
		return n, "llm", err
	}
	doc := Document{Path: url, Title: title, Fragments: TextFragments(data)}
	if err := w.Store.Ingest(ctx, doc); err != nil {
		sl.Fail("segment", "offline", err)
		return 0, "offline", err
	}
	sl.Done("segment", "offline", fmt.Sprintf("%d fragment(s)", len(doc.Fragments)))
	sl.Done("commit", "", "")
	return len(doc.Fragments), "offline", nil
}

// writeTemp materializes bytes to a temp file with the given extension (external
// tools — pandoc, poppler — read files and detect format by extension), and
// returns a cleanup func.
func writeTemp(data []byte, ext string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "raglit-*"+ext)
	if err != nil {
		return "", func() {}, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// TextFragments splits raw text/markdown into fragments on blank lines
// (paragraph grain). Pageless (page 0). Shared by the CLI text path and the
// worker so both fragment identically.
func TextFragments(data []byte) []Fragment {
	var frags []Fragment
	ord := 0
	for _, block := range strings.Split(string(data), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		frags = append(frags, Fragment{Ord: ord, Text: block})
		ord++
	}
	return frags
}
