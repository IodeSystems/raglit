package raglit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Reading a page again after the first read was wrong.
//
// The OCR page cache is keyed by the image's SHA and nothing else, so
// re-indexing a document renders the same pixels, computes the same key, and
// gets the same answer back. That is exactly what you want when the answer was
// right — it is why a re-ingest of a 200-page scan costs nothing — and it means
// A BAD READ IS PERMANENT.
//
// Observed, and it is what this exists for. A six-page summary-judgment order
// transcribed to nothing but its diagonal "UNOFFICIAL DOCUMENT" watermark. Five
// documents in one corpus were re-indexed to fix reads like it; four of the five
// "completed" in twenty seconds and not one byte changed, because every page was
// a cache hit. The transcription plausibility check will now FLAG those pages,
// but flagging without a way to redo the read is just a label.
//
// So: purge the cached pages for one document, then let the ordinary ingest path
// run. Nothing else is special-cased — the point is to make the normal path do
// the work again, not to build a second one.

// PurgeDocPageCache drops the cached OCR for every page of one document and
// returns how many entries it removed.
//
// It works by rendering the document exactly as ingest does and computing the
// same keys, which is sound because the render is deterministic at a fixed dpi
// (measured, not assumed). A page whose render has changed — a different poppler,
// a re-exported PDF — simply will not match, and the miss is harmless: the page
// was going to be re-read anyway.
func (s *Store) PurgeDocPageCache(ctx context.Context, path string) (int, error) {
	if ClassifyDoc(path, "") != KindPDF {
		// Only rasterised formats use the page cache. Text, office and email go
		// nowhere near it, so there is nothing to purge and saying so beats
		// reporting a confident zero.
		return 0, fmt.Errorf("%s is not a rasterised document — the page cache does not apply", path)
	}
	units, err := pdfUnits(ctx, path, false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range units {
		if !u.isImage() {
			continue
		}
		res, err := s.db.Exec(`DELETE FROM ocr_page_cache WHERE img_sha = ?`, pageImageSHA(u.data))
		if err != nil {
			return n, fmt.Errorf("purge page %d: %w", u.page, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n += int(c)
		}
	}
	return n, nil
}

// SuspectDocs returns the documents under root whose transcription has at least
// one page that does not look like a page, with the page numbers.
//
// Reads the SIDECARS rather than the index: the sidecar is what a human and
// kgraph both actually consume, so a document whose sidecar is wrong is wrong
// for every purpose that matters even if the index rows happen to be fine.
func SuspectDocs(root string) (map[string][]int, error) {
	out := map[string][]int{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !IsTranscription(p) {
			return nil //nolint:nilerr // an unreadable corner must not stop the sweep
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		var bad []int
		for _, pg := range splitTranscriptionPages(string(b)) {
			// A page carrying a described figure is exempt — a survey sheet is
			// mostly drawing and the description IS the read.
			if strings.Contains(pg.text, "> **") {
				continue
			}
			if Suspicion(pg.text) != "" {
				bad = append(bad, pg.page)
			}
		}
		if len(bad) > 0 {
			out[strings.TrimSuffix(p, transcriptionSuffix)] = bad
		}
		return nil
	})
	return out, err
}

var transcriptionPageMark = regexp.MustCompile(`(?m)^##+\s*Page\s+(\d+)\s*$`)

type transcribedSlice struct {
	page int
	text string
}

// splitTranscriptionPages cuts a sidecar at its page markers.
//
// Reads the markers rather than counting sections, because a sidecar may start
// at a page other than one and a count would then mislabel every page after a
// gap — and the page number is the whole value of the marker.
func splitTranscriptionPages(s string) []transcribedSlice {
	locs := transcriptionPageMark.FindAllStringSubmatchIndex(s, -1)
	out := make([]transcribedSlice, 0, len(locs))
	for i, m := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		n, err := strconv.Atoi(s[m[2]:m[3]])
		if err != nil {
			continue
		}
		out = append(out, transcribedSlice{page: n, text: s[m[1]:end]})
	}
	return out
}
