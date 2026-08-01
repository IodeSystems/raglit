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
	// Segmenter LLM-segments a document ONLY when a page escalated to the VLM (the
	// llm-seg path, decided per-document in ingestUnits). Text/code never escalate,
	// so they take the deterministic overlapping-window fragmenter regardless of
	// whether a model is configured. nil → PDF/image llm-seg unavailable.
	Segmenter *Segmenter
	// Frag tunes the deterministic text fragmenter (window/stride/floor) and caps
	// the fragment ceiling by the embed model's probed input limit. Zero → defaults.
	Frag FragConfig
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
	// Retries collects what the LLM client had to survive for the job in flight,
	// recorded as a stage row when it is not empty. nil → retries stay invisible,
	// which is what they were.
	Retries *RetryTally
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
	// Start this job's retry history empty: whatever the previous job survived is
	// its own, and a tally that carries over blames the wrong document.
	w.Retries.Take()
	n, mode, ierr := w.ingest(ctx, job, sl)
	// Recorded LAST, so it covers the whole job, and on the failure path too —
	// a job that died after forty minutes of backpressure is exactly the one
	// whose retry history has to survive it.
	if s := w.Retries.Take(); !s.Empty() {
		state := "done"
		if s.GaveUp > 0 || ierr != nil {
			state = "warn"
		}
		sl.Record("llm-retries", "", state, s.Detail())
	}
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
// via sl and returning (fragment count, fragmenter mode, error). mode is the
// per-document fragmenter chosen: "text-overlap" (deterministic) or "llm-seg" (a
// page escalated to the VLM), or "pooled"/"unchanged" for the reuse/skip paths.
func (w *Worker) ingest(ctx context.Context, job *Job, sl *StageLog) (int, string, error) {
	// A withdrawn document is not fetched, not read, and not indexed.
	//
	// Checked FIRST, before the fetch, because the cheapest correct answer is to
	// do none of the work. And checked here at all because a withdrawal that only
	// deleted rows would last until the next file change: the watcher re-queues
	// on change, the worker re-indexes, and the ruling silently expires. The
	// decision has to be enforced where documents enter, not only where they were
	// removed.
	if reason, ok := w.Store.WithdrawnReason(job.URL); ok {
		sl.Skip("withdrawn", reason)
		return 0, "withdrawn", nil
	}
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

	// Route BEFORE anything caches on it. What this document is read AS is part
	// of what the cached result means, so the decision has to exist before the
	// key that stores it — see poolRecipe.
	kind := w.route(job, f)

	// Fast path — same index, identical bytes: skip entirely (nothing to do).
	// --fresh skips it, because "nothing changed" is exactly what a caller
	// re-reading a document already knows and is overriding.
	if !job.Fresh {
		if prev, _ := w.Store.DocumentHash(job.URL); prev != "" && prev == hash {
			sl.Skip("extract", "unchanged — source hash match")
			return 0, "unchanged", nil
		}
	}

	// Cross-index reuse — this (recipe, file) was processed anywhere before: copy
	// the cached fragments + vectors + page images in (cheap), no LLM.
	if !job.Fresh && w.Pool != nil && w.RecipeHash != "" {
		if doc, ok, _ := w.Pool.Get(w.poolRecipe(kind), hash); ok {
			t := title
			if job.Title == "" && doc.Title != "" {
				t = doc.Title
			}
			if n, err := w.Store.IngestPooled(ctx, job.URL, t, doc, w.Pool.FileDir(hash)); err == nil {
				_ = w.Store.SetDocumentHash(job.URL, hash)
				sl.Skip("extract", "pooled — reused cached processing (recipe+kind+file match)")
				return n, "pooled", nil
			}
			// copy failed → fall through and reprocess.
		}
	}

	// Process fresh, then remember it (per-index hash + shared pool).
	n, mode, ierr := w.extractAndIngestAs(ctx, job, f, kind, title, sl)
	if ierr == nil {
		_ = w.Store.SetDocumentHash(job.URL, hash)
		if w.Pool != nil && w.RecipeHash != "" {
			if doc, e := w.Store.ExportDoc(job.URL); e == nil {
				_ = w.Pool.Put(w.poolRecipe(kind), hash, doc)
			}
		}
	}
	return n, mode, ierr
}

// route decides what a fetched document is, once per ingest.
func (w *Worker) route(job *Job, f Fetched) DocKind {
	kind := ClassifyDoc(job.URL, f.ContentType)
	if kind == KindUnknown {
		// Nothing in the name or the content type said what this is, and the
		// text fall-through would index a PDF's `%PDF-1.7` and `endobj` while the
		// document stayed unsearchable — silently, because reading bytes as text
		// never fails. The first eight bytes usually know.
		kind = SniffBytes(f.Data)
	}
	if f.IsPDF {
		kind = KindPDF
	}
	return kind
}

// poolRecipe folds the ROUTING DECISION into the pool key.
//
// Without it a misrouted read is permanent. The pool is keyed by (recipe, file
// bytes), and the recipe captured the models and the fragmenter — everything
// that shapes the output EXCEPT the choice of which reader produced it. So a PDF
// once read as plain text cached `%PDF-1.7` under those bytes, and every later
// ingest of that content, on any path and in any index, replayed the cached
// garbage. Fixing the router changed nothing, and nothing said so: the job
// reported `done`.
//
// A routing change must therefore be a cache miss, which is what including the
// kind buys. The cost is one extra entry for any file whose kind legitimately
// changes, which is rare and is the case where reprocessing is correct anyway.
func (w *Worker) poolRecipe(kind DocKind) string {
	if w.RecipeHash == "" {
		return ""
	}
	return HashHex([]byte(w.RecipeHash + "|kind=" + kind.String()))
}

// extractAndIngest routes a fetched document by kind (extract.go) and indexes it:
// a PDF runs the text-layer/OCR hybrid, an office/markup file goes through
// pandoc, an image through OCR, and anything else is treated as text.
func (w *Worker) extractAndIngest(ctx context.Context, job *Job, f Fetched, title string, sl *StageLog) (int, string, error) {
	return w.extractAndIngestAs(ctx, job, f, w.route(job, f), title, sl)
}

// extractAndIngestAs is extractAndIngest with the routing already decided, so
// the kind the pool keyed on is the kind that actually reads the document. Two
// separate decisions could disagree, and a cache key that describes a different
// read than the one performed is worse than no key at all.
func (w *Worker) extractAndIngestAs(ctx context.Context, job *Job, f Fetched, kind DocKind, title string, sl *StageLog) (int, string, error) {
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
		// ingestPDF records the extract + ocr + segment + embed + commit stages, and
		// picks the fragmenter per-document (llm-seg if a page hit the VLM, else
		// text-overlap). mode is the fragmenter it chose.
		return w.Store.ingestPDF(ctx, w.OCR, job.URL, path, title, w.Frag, sl)

	case KindImage:
		if w.OCR == nil {
			err := fmt.Errorf("image ingest needs a vision model — configure one (raglit init) and serve with OCR")
			sl.Fail("extract", "image", err)
			return 0, "", err
		}
		mime, data := mimeForExt(filepath.Ext(job.URL)), f.Data
		// HEIC/HEIF needs a real file on disk — the converter is a CLI tool, not
		// a library call — so only pay for writeTemp on the format that needs it.
		if heicExts[strings.ToLower(filepath.Ext(job.URL))] {
			path, cleanup, err := writeTemp(f.Data, strings.ToLower(filepath.Ext(job.URL)))
			if err != nil {
				return 0, "", err
			}
			defer cleanup()
			if data, err = HEICToPNG(ctx, path); err != nil {
				sl.Fail("extract", "image", err)
				return 0, "", err
			}
			mime = "image/png"
		}
		sl.Done("extract", "image", "1 page")
		units := []ingestUnit{{page: 1, mime: mime, data: data}}
		// ingestUnits OCRs the image → text, then fragments it (records ocr/segment/…).
		return w.Store.ingestUnits(ctx, NewSegmenter(w.OCR.Client), w.OCR, job.URL, title, units, w.Frag, sl)

	case KindEmail:
		// The extension here is raglit's, not the corpus's — an .mbox fetched by
		// URL lands in a raglit-*.eml temp file all the same. The reader sniffs
		// the RFC 4155 signature rather than trusting a name it did not choose.
		path, cleanup, err := writeTemp(f.Data, ".eml")
		if err != nil {
			return 0, "", err
		}
		defer cleanup()
		pages, err := EmailText(path)
		if err != nil {
			sl.Fail("extract", "email", err)
			return 0, "", err
		}
		sl.Done("extract", "email", fmt.Sprintf("%d message(s)", len(pages)))
		w.extractAttachments(job.URL, path, sl)
		return w.ingestPaged(ctx, job.URL, title, pages, sl)

	case KindSpreadsheet:
		ext := strings.ToLower(filepath.Ext(job.URL))
		path, cleanup, err := writeTemp(f.Data, ext)
		if err != nil {
			return 0, "", err
		}
		defer cleanup()
		pages, err := SpreadsheetPages(ctx, path)
		if err != nil {
			sl.Fail("extract", "spreadsheet", err)
			return 0, "", err
		}
		sl.Done("extract", "spreadsheet", fmt.Sprintf("%d sheet(s)", len(pages)))
		return w.ingestPaged(ctx, job.URL, title, pages, sl)

	case KindOffice:
		path, cleanup, err := writeTemp(f.Data, strings.ToLower(filepath.Ext(job.URL)))
		if err != nil {
			return 0, "", err
		}
		defer cleanup()
		text, err := OfficeText(ctx, path)
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

// extractAttachments writes a mail archive's attachments beside it, when this
// archive's project asked for that. url is the document's identity in the
// corpus; srcPath is the temp file holding the bytes.
//
// Best-effort, exactly like the transcription writeback: a sidecar that could
// not be written — a read-only corpus, a mount that went away — must not fail an
// ingest that otherwise read the archive correctly. The failure is recorded as a
// stage so it is visible rather than assumed.
func (w *Worker) extractAttachments(url, srcPath string, sl *StageLog) {
	corpus := archiveCorpusPath(url)
	if corpus == "" || !extractAttachmentsForDoc(corpus, w.Store.extractEmailAttachments) {
		return
	}
	n, dir, err := ExtractEmailAttachments(srcPath, corpus)
	switch {
	case err != nil:
		sl.Fail("attachments", "email", err)
	case n > 0:
		sl.Done("attachments", "email", fmt.Sprintf("%d file(s) → %s/", n, filepath.Base(dir)))
	default:
		sl.Skip("attachments", "no attachments in this archive")
	}
}

// ingestPlainText fragments a text/code document with the deterministic
// overlapping-window fragmenter (fragment.go) — text never escalates to a model,
// so there is no LLM fork: the same path runs whether or not a model is
// configured. mode is the fragmenter chosen ("text-overlap").
func (w *Worker) ingestPlainText(ctx context.Context, url, title string, data []byte, sl *StageLog) (int, string, error) {
	return w.Store.ingestText(ctx, url, title, string(data), w.Frag, sl)
}

// ingestPaged indexes a document that already knows its own pages, keeping the
// page numbers.
//
// Email and spreadsheets both arrive pre-paginated for the same reason — one
// page per nested message, one per sheet — and both used to be flattened into a
// single string with "## Page N" written into the text, then handed to the plain
// text path. That preserved the pagination as CHARACTERS and lost it as
// metadata: every fragment came back page 0, so a quotation from the fifth
// message of a 24-message archive could be located only somewhere in 105,000
// characters. The marker in the text says the page; nothing could query it.
//
// The 24 MB broker log is the case that shows it: 24 nested messages, ingested
// as one page. `similar` reports its alignments as "p0", `raglit slice` cannot
// name a range, and "which message said that" has no answer.
func (w *Worker) ingestPaged(ctx context.Context, url, title string, pages []PageText, sl *StageLog) (int, string, error) {
	units := make([]ingestUnit, 0, len(pages))
	for _, pg := range pages {
		units = append(units, ingestUnit{page: pg.Page, text: pg.Text})
	}
	return w.Store.ingestUnits(ctx, w.Segmenter, w.OCR, url, title, units, w.Frag, sl)
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
