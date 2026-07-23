package raglit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	n, ierr := w.ingest(ctx, job)
	if ierr != nil {
		return true, w.Store.failJob(job.ID, ierr.Error())
	}
	return true, w.Store.completeJob(job.ID, n)
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

// ingest turns a job's URL into indexed fragments, returning the count.
func (w *Worker) ingest(ctx context.Context, job *Job) (int, error) {
	f, err := w.fetch(ctx, job.URL)
	if err != nil {
		return 0, err
	}
	title := job.Title
	if title == "" {
		title = f.Title
	}

	// Route by document kind (extract.go): a PDF runs the text-layer/OCR hybrid,
	// an office/markup file goes through pandoc, an image through OCR, and anything
	// else is treated as text.
	kind := ClassifyDoc(job.URL, f.ContentType)
	if f.IsPDF {
		kind = KindPDF
	}
	switch kind {
	case KindPDF:
		if w.OCR == nil {
			return 0, fmt.Errorf("pdf ingest needs a vision model — configure one (raglit init) and serve with OCR")
		}
		path, cleanup, err := writeTemp(f.Data, ".pdf")
		if err != nil {
			return 0, err
		}
		defer cleanup()
		return w.Store.ingestPDF(ctx, w.OCR, job.URL, path, title)

	case KindImage:
		if w.OCR == nil {
			return 0, fmt.Errorf("image ingest needs a vision model — configure one (raglit init) and serve with OCR")
		}
		mime := mimeForExt(filepath.Ext(job.URL))
		units := []ingestUnit{{page: 1, mime: mime, data: f.Data}}
		return w.Store.ingestUnits(ctx, NewSegmenter(w.OCR.Client), job.URL, title, units)

	case KindOffice:
		path, cleanup, err := writeTemp(f.Data, strings.ToLower(filepath.Ext(job.URL)))
		if err != nil {
			return 0, err
		}
		defer cleanup()
		text, err := PandocText(ctx, path)
		if err != nil {
			return 0, err
		}
		return w.ingestPlainText(ctx, job.URL, title, []byte(text))
	}

	// KindText / KindUnknown: read as text.
	return w.ingestPlainText(ctx, job.URL, title, f.Data)
}

// ingestPlainText segments text with the LLM segmenter when configured, else
// falls back to a blank-line paragraph split (fully offline).
func (w *Worker) ingestPlainText(ctx context.Context, url, title string, data []byte) (int, error) {
	if w.Segmenter != nil {
		return w.Store.ingestText(ctx, w.Segmenter, url, title, string(data), w.WindowChars)
	}
	doc := Document{Path: url, Title: title, Fragments: TextFragments(data)}
	if err := w.Store.Ingest(ctx, doc); err != nil {
		return 0, err
	}
	return len(doc.Fragments), nil
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
