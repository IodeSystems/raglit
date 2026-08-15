package raglit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
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
	// STT transcribes audio jobs, by asking oidio. nil → an audio job fails with
	// a clear message, the same way a PDF job does with no vision model: a corpus
	// of documents needs no transcriber and should not be told to configure one.
	STT *STT
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
	return true, w.ProcessJob(ctx, job)
}

// ProcessJob runs a job that has ALREADY been claimed.
//
// Split from ProcessOne so a scheduler can separate the two halves. Claiming is
// a transaction; running is minutes of OCR. The lane dispatcher claims in a
// round-robin over every index — which is what makes it fair — and hands the job
// to whichever slot is free, so one long job occupies a slot instead of the
// whole queue. ProcessOne stays the claim-and-run pair the CLI drain and the
// tests use.
func (w *Worker) ProcessJob(ctx context.Context, job *Job) error {
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
		return w.Store.failJob(job.ID, ierr.Error())
	}
	return w.Store.completeJob(job.ID, n, mode)
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

	// An empty file, named as an empty file.
	//
	// Checked here — before routing, before every extractor — because zero bytes
	// is a fact about the SOURCE, and each reader discovers it separately and
	// says something else. pandoc returned `exit status 63` on a 0-byte .docx in
	// the delano corpus, which names the tool and not the problem and sends
	// somebody to check whether pandoc is installed. A 0-byte PDF gets a poppler
	// error; a 0-byte .txt gets no error at all and indexes as a document with no
	// fragments, which is the `no-fragments` health kind — a row that looks like
	// a document from every angle except the one that matters.
	//
	// SKIPPED, not failed. Having no content is a fact about the document; being
	// unable to import it is a fact about the importer, and reporting the second
	// when the first is true sends the reader to the wrong half. There is nothing
	// wrong with this ingest — it read the file correctly and the file is empty.
	//
	// So the job completes, with mode `empty`, and the emptiness is reported as a
	// PROBLEM instead (ProblemEmptySource). That keeps it out of the failed-jobs
	// list, where it would be retried forever by anything that retries failures
	// and would keep re-failing identically, while still being visible — three
	// copies of the same empty `Order on Motion to Continue.docx` sit in that
	// evidence folder, and the document they should carry is absent from the
	// record. Absent and flagged is recoverable; absent and quiet is not.
	if len(f.Data) == 0 {
		sl.Skip("extract", "the file is empty (0 bytes) — nothing to index")
		return 0, "empty", nil
	}

	hash := sha256hex(f.Data)
	title := job.Title
	if title == "" {
		title = f.Title
	}

	// Route BEFORE anything caches on it. What this document is read AS is part
	// of what the cached result means, so the decision has to exist before the
	// key that stores it — see poolRecipe.
	kind := w.route(job, f)

	// Correct the lane now that the bytes have been read.
	//
	// Enqueue could only guess from the URL (lane.go), and an extensionless file
	// that sniffs to a scan was guessed light. The job is already claimed and
	// this does not move it — it runs where it is, which is the cost of guessing
	// — but the row now says what it actually was, so a retry is claimed by the
	// right lane and the queue's own report stops lying about its shape.
	if got := LaneForKind(kind); got != LaneFor(job.URL) {
		_ = w.Store.SetJobLane(job.ID, got)
		sl.Record("lane", string(got), "done",
			fmt.Sprintf("reclassified from %s: the name said nothing, the bytes are %s",
				LaneFor(job.URL), kind))
	}

	// Refuse what has no representation to index — BEFORE the caches.
	//
	// KindUnknown means two different things and the reader cannot tell them
	// apart: "text whose extension I do not recognise" and "a compiled binary".
	// The fall-through reads both as text, and reading bytes as text never
	// fails, so a 27 MB executable indexed cleanly into 4,657 fragments and
	// reported `done`.
	//
	// It sits above the pool on purpose. The first version of this guard was
	// inside extractAndIngest and never fired: the pooled entry from the earlier
	// bad ingest matched (same recipe, same kind, same bytes) and replayed the
	// cached garbage — precisely the permanence poolRecipe's own comment
	// describes for a misrouted read. Whether a file can be indexed at all is a
	// property of its bytes, so it must not depend on cache state.
	if kind == KindUnknown {
		if IsOpaque(f.Data) {
			// Say which of the two refusals this is. Now that IsOpaque stops media
			// as well as binaries, "has no text or image representation" is false
			// about a recording — an .mp4 of a hearing plainly has speech in it,
			// and raglit's inability to read it is a fact about raglit. Telling a
			// reviewer their recording contains nothing sends them looking for a
			// corrupt file that is fine, which is the same failure audioEvidenceFor
			// avoids by returning nil rather than a renderer that always errors.
			ct := http.DetectContentType(f.Data)
			if strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") {
				sl.Skip("extract", "media — raglit has no audio/video extractor ("+ct+")")
				return 0, "", fmt.Errorf("raglit: %s is %s — raglit cannot read audio or video; "+
					"transcribe it first and index the transcript",
					filepath.Base(job.URL), ct)
			}
			sl.Skip("extract", "no text/image representation — "+ct)
			return 0, "", fmt.Errorf("raglit: %s has no text or image representation to index "+
				"(sniffed %s) — refusing rather than indexing its bytes as text",
				filepath.Base(job.URL), ct)
		}
		// Text, but with no line structure a human put there: a minified bundle
		// or a generated blob. It would fragment into chunks of unreadable
		// machine output and embed every one.
		if IsMinified(f.Data) {
			sl.Skip("extract", "minified/generated — no line structure to fragment on")
			return 0, "", fmt.Errorf("raglit: %s looks machine-generated (a line of %d+ characters) "+
				"— refusing rather than embedding minified output",
				filepath.Base(job.URL), minifiedLineBytes)
		}
	}

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
				// A pooled document is a READING of these bytes exactly as a fresh
				// one is — the same fragments, the same pages, produced by the same
				// recipe. It was the one ingest path that recorded none, so a
				// document's trust depended on which index happened to read it
				// first: the first got `vision-ocr, text 90%, subject 80%` and every
				// index that reused it got nothing at all.
				//
				// Recorded from the pages just committed, so it says what this index
				// actually holds. Payloads pooled before the measurement existed
				// carry no per-page counts and are recorded UNMEASURED rather than
				// as zero.
				if kind != KindAudio {
					w.recordIngestReading(job.URL, hash, kind, sl)
				}
				sl.Skip("extract", "pooled — reused cached processing (recipe+kind+file match)")
				return n, "pooled", nil
			}
			// copy failed → fall through and reprocess.
		}
	}

	// Process fresh, then remember it (per-index hash + shared pool).
	n, mode, ierr := w.extractAndIngestAs(ctx, job, f, kind, title, sl)
	if ierr == nil {
		// What this ingest READ, recorded so a later account of the same bytes
		// can find it (readings.go).
		//
		// The audio path records its own, because it knows the structure it
		// produced. Everything else is registered here, at the one point that
		// holds the source digest and the routing decision together — and the
		// digest is the whole value: a corrected transcription of this document,
		// or a region descent over the same sheet, is a reading of the SAME
		// bytes, and that is what lets the two be compared instead of sitting in
		// the index as unrelated documents.
		if kind != KindAudio {
			w.recordIngestReading(job.URL, hash, kind, sl)
		}
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
	if kind == KindUnknown {
		// A recording with no extension, fetched from somewhere that served no
		// content type. fileMagic does not carry media signatures — ISO-BMFF's
		// ftyp box is at a variable offset with a brand list, which is more than a
		// byte-prefix table can express — but the stdlib's WHATWG sniffer knows
		// them, and it is already being called a few lines later by IsOpaque.
		//
		// Without this the file reaches IsOpaque, which refuses it as having no
		// representation to index. That was the RIGHT answer while raglit had no
		// transcriber and is the wrong one now that oidio is wired in: the bytes
		// are readable, only the name failed to say so.
		if ct := http.DetectContentType(f.Data); strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") {
			kind = KindAudio
		}
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
// segmenter is the worker's configured segmenter, falling back to one built on
// the OCR client so a caller that never set the field behaves as it always did.
func (w *Worker) segmenter() *Segmenter {
	if w.Segmenter != nil {
		return w.Segmenter
	}
	if w.OCR == nil {
		return nil
	}
	return NewSegmenter(w.OCR.Client)
}

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
		return w.Store.ingestPDF(ctx, w.segmenter(), w.OCR, job.URL, path, title, w.Frag, sl)

	case KindAudio:
		if w.STT == nil {
			err := fmt.Errorf("audio ingest needs a transcriber — configure audio_base_url + audio_model (oidio) and retry")
			sl.Fail("extract", "audio", err)
			return 0, "", err
		}
		return w.ingestAudio(ctx, job, f, title, sl)

	case KindImage:
		if w.OCR == nil {
			err := fmt.Errorf("image ingest needs a vision model — configure one (raglit init) and serve with OCR")
			sl.Fail("extract", "image", err)
			return 0, "", err
		}
		// An image too small to be a page is not a document. See
		// ImageTooSmallToBeAPage: a mail signature logo arrives through the
		// attachment path looking exactly like a scan, and 31 of them were
		// indexed — and captioned — in one corpus before anything asked.
		if small, dims := ImageTooSmallToBeAPage(f.Data); small {
			sl.Done("extract", "image", "skipped: "+dims+" is too small to be a page")
			return 0, "", nil
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
		// w.Segmenter, NOT a fresh one off the OCR client: this path built its own
		// and so silently ignored a configured segment model, which is most of
		// the corpus (every PDF and image). The other ingest path already used
		// the field; only this one did not.
		return w.Store.ingestUnits(ctx, w.segmenter(), w.OCR, job.URL, title, units, w.Frag, sl)

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
	// Into RAGLIT's storage, not beside the archive. See Home.AttachmentDir.
	dest := w.Store.AttachmentDirFor(corpus)
	if dest == "" {
		sl.Skip("attachments", "this index has no home to store attachments in")
		return
	}
	n, dir, err := ExtractEmailAttachments(srcPath, corpus, dest)
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


// recordIngestReading registers what an ordinary (non-audio) ingest read.
//
// A document is its OWN source here — the bytes fetched are the asset — so the
// degenerate case is the common one and it is still worth recording: the row is
// what a second reading of those bytes joins on.
//
// Best-effort. A reading that could not be registered must not fail an ingest
// that produced a good document; the stage says so rather than the failure being
// inferred from a missing row later.
func (w *Worker) recordIngestReading(docPath, hash string, kind DocKind, sl *StageLog) {
	method := MethodVerbatim
	switch kind {
	case KindPDF, KindImage:
		method = MethodVision
	case KindOffice, KindSpreadsheet:
		method = MethodPandoc
	case KindEmail:
		method = MethodEmail
	}
	txt, terr := w.Store.DocText(docPath, 0, 0, 0)
	text := ""
	if terr == nil {
		text = txt.Text
	}
	// What actually read this document, and how much of it a model made up.
	//
	// Both come from the PAGES, and both used to come from somewhere that could
	// not know:
	//
	//   - The method was taken from the fragmenter's mode, which reports how the
	//     text was CHOPPED, not where it came from. `text-overlap` is also what a
	//     vision read falls back to when the LLM segmenter drops text, so the SMS
	//     exhibit — 15 pages, every one read by chandra — was recorded as
	//     `text-layer` and its trust went UP, from a model's 90 to an exact 100.
	//   - The described fraction was measured over the page text held here, and
	//     the evidence for it is the layout markup, which is stripped before that
	//     text is stored. It could only ever have measured 0.
	//
	// So both read the per-page rows, which the pipeline writes while it still
	// has the markup and the engine in hand. Weighted by characters, not by page
	// count: one photograph in a forty-page bundle is a transcription.
	describes, pct := false, 0
	if _, pages, err := w.Store.DocReview(docPath); err == nil && len(pages) > 0 {
		var chars, described, vision, measured int
		for _, pg := range pages {
			chars += pg.TextChars
			described += pg.DescribedChars
			if pg.TextChars > 0 {
				measured++
			}
			if pg.Vision {
				vision++
			}
		}
		switch {
		case measured == 0:
			// Pages exist but none carries a measurement: rows written before the
			// columns did, or a pool payload from the same era. UNMEASURED is not
			// zero, and recording it as zero would assert "a model made none of
			// this up" about the documents most likely to be screenshots. Said out
			// loud so a backfill can find them.
			pct = DescribedUnmeasured
		case chars > 0:
			pct = described * 100 / chars
			describes = pct >= int(describedPageThreshold*100)
		}
		// A page a model read is a page a model read, whatever the rest of the
		// document did — the weaker claim governs, because a reader quoting the
		// document has no way to know which page a sentence came from. The engine
		// is reliable even on old rows, so this does not depend on `measured`.
		if kind == KindPDF || kind == KindImage {
			if vision > 0 {
				method = MethodVision
			} else {
				method = MethodTextLayer
			}
		}
	}
	if err := w.Store.RecordReading(Reading{
		SourceSHA256: hash, SourcePath: docPath, DocPath: docPath,
		Method: method, Level: ReadingMachine, Text: text,
		Describes: describes, DescribedPct: pct,
	}); err != nil {
		sl.Fail("reading", "index", err)
	}
}
