package raglit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
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
// startOff/endOff are the half-open source span (text-overlap path); 0/0 on the
// llm-seg path, where a fragment is model-emitted text and not a span.
type stagedFrag struct {
	page, ord        int
	startOff, endOff int
	text             string
	// pageSpans is where each page's content begins inside text, for a fragment
	// that spans pages. Nil when it lies on one page.
	pageSpans []PageSpan
}

// stagedPage is a page's provenance held in memory until the atomic swap.
type stagedPage struct {
	page    int
	engine  string
	imgPath string
}

// stagedMedia is a figure explained into a fragment, anchored by the fragment's
// index in the staged slice (its id doesn't exist until the swap). vec/space are
// the figure's embedding, filled by embedMedia before the swap (empty vec → no
// embedding, e.g. no embedder or an embed failure — the figure is still stored).
type stagedMedia struct {
	fragIdx     int
	page, ord   int
	kind        string
	imagePath   string
	bbox        string
	description string
	vec         []float32
	space       string
}

// FragConfig tunes the deterministic text fragmenter and identifies the per-
// document fragmentation recipe. Window/Stride/Floor are 0 → defaults; EmbedLimit
// caps the window by the embed model's input limit (0 → uncapped); FigurePrompt
// is the figure-description prompt version, folded into frag_recipe so a prompt
// change marks affected documents stale.
type FragConfig struct {
	Window, Stride, Floor int
	EmbedLimit            int
	FigurePrompt          int
}

// fragMode names: text-overlap (deterministic windows) vs llm-seg (a VLM already
// transcribed a page, so the model segments too).
const (
	fragModeOverlap = "text-overlap"
	fragModeLLM     = "llm-seg"
)

// fragRecipe hashes ONLY the fragmentation inputs for a document — deliberately
// narrower than the pool recipe (which also mixes in the embed model + OCR
// engine) — so it answers "which documents need re-fragmenting after a stride
// change" without dragging in unrelated model swaps.
func fragRecipe(mode string, window, stride, floor, figPrompt int) string {
	if mode == fragModeLLM {
		return HashHex([]byte(fmt.Sprintf("mode=%s|fig=%d", mode, figPrompt)))
	}
	return HashHex([]byte(fmt.Sprintf("mode=%s|w=%d|s=%d|f=%d|fig=%d", mode, window, stride, floor, figPrompt)))
}

// resolvedPage is one unit's text after OCR, plus its page number.
type resolvedPage struct {
	page int
	text string
}

// ingestUnits runs the per-document pipeline and commits atomically. It resolves
// every unit to text FIRST (image units run the cheap→gate→VLM OCR cascade, the
// "ocr" task, tagged with the real engine per page), then makes ONE per-document
// fragmenter choice: if any page escalated to the VLM the document is LLM-
// segmented as a whole (fragMode "llm-seg", segment.go); otherwise the
// deterministic overlapping-window fragmenter runs ("text-overlap", fragment.go)
// — text needs no model. Embedding ("embed") runs concurrently with segmentation,
// and fragments + vectors + provenance + media are swapped in under one
// transaction ("commit"). ocr may be nil when no unit is an image; sg may be nil
// when no page will escalate (pure text).
func (s *Store) ingestUnits(ctx context.Context, sg *Segmenter, ocr *OCR, docPath, title string, units []ingestUnit, fc FragConfig, sl *StageLog) (int, string, error) {
	// Phase 1: resolve every unit to text + provenance. The vision escalation is
	// what decides the fragmenter, so it must be known before segmenting — one
	// pass over the page engines, which pipeline already tallies.
	var pages []resolvedPage
	var provenance []stagedPage
	ocrEngines := map[string]int{}
	cachedHits := 0
	sawVision := false
	for _, u := range units {
		text := u.text
		if u.isImage() {
			if ocr == nil {
				sl.Fail("ocr", "", fmt.Errorf("page %d is an image but no OCR is configured", u.page))
				return 0, "", fmt.Errorf("page %d is an image but no OCR is configured", u.page)
			}
			t, engine, fromCache, err := s.ocrPageCached(ctx, ocr, PageImage{Page: u.page, Mime: u.mime, Data: u.data})
			if err != nil {
				// Pages already transcribed on this run are in the cache, so the
				// retry resumes here rather than starting the document again.
				sl.Fail("ocr", engine, fmt.Errorf("page %d: %w (%d page(s) already cached and will not be re-read)", u.page, err, cachedHits))
				return 0, "", err
			}
			if fromCache {
				cachedHits++
			}
			text = t
			ocrEngines[engine]++
			if engine == "vision" {
				sawVision = true
			}
			// Save the page image + record provenance with the REAL cascade engine
			// (tesseract/paddleocr/vision), so review shows which pages escalated.
			imgPath := ""
			if p, e := s.savePageImage(docPath, u.page, u.mime, u.data); e == nil {
				imgPath = p
			}
			provenance = append(provenance, stagedPage{page: u.page, engine: engine, imgPath: imgPath})
		} else if u.page >= 1 {
			// A born-digital / text-layer page: no OCR, engine "text".
			provenance = append(provenance, stagedPage{page: u.page, engine: "text"})
		}
		pages = append(pages, resolvedPage{page: u.page, text: text})
	}
	// The transcription, if this index asked for one. Written here because `pages`
	// is exactly the per-page text a transcription is, and after fragmentation it
	// is gone. Best-effort: a convenience file must never fail a good ingest.
	if docPath != "" && writebackForDoc(docPath, s.writebackTranscription) {
		tp := make([]TranscribedPage, 0, len(pages))
		for _, p := range pages {
			if p.page >= 1 {
				tp = append(tp, TranscribedPage{Page: p.page, Text: p.text})
			}
		}
		if len(tp) > 0 {
			// Re-issue whatever a person corrected. An ingest that wrote an
			// uncorrected export would undo checked work exactly as the old
			// unconditional overwrite did.
			corr := correctionsForDoc(docPath)
			if out, err := WriteTranscriptionCorrected(docPath, tp, corr); err != nil {
				sl.Fail("transcribe", "", err)
			} else {
				detail := filepath.Base(out)
				if len(corr) > 0 {
					detail += fmt.Sprintf(" (%d corrected page(s) re-issued)", len(corr))
				}
				sl.Done("transcribe", "", detail)
			}
		}
	}
	if len(ocrEngines) > 0 {
		n := 0
		for _, c := range ocrEngines {
			n += c
		}
		msg := fmt.Sprintf("%d page(s)", n)
		if cachedHits > 0 {
			msg += fmt.Sprintf(", %d from cache", cachedHits)
		}
		sl.Done("ocr", engineSummary(ocrEngines), msg)
	}

	// Concurrent embed pipeline (only when a store has an embedder). It embeds
	// finalized fragments while later units segment, holding the vectors in memory
	// (keyed by fragment index) — written in the final swap, not as produced.
	var embedCh chan embedItem
	var embedDone chan embedResult
	if s.embedder != nil {
		embedCh = make(chan embedItem, 64)
		embedDone = make(chan embedResult, 1)
		go runStagedEmbed(ctx, s.embedder, embedCh, embedDone)
	}
	var frags []stagedFrag
	sink := func(f stagedFrag) {
		idx := len(frags)
		frags = append(frags, f)
		if embedCh != nil {
			embedCh <- embedItem{idx: idx, text: f.text}
		}
	}
	drainEmbed := func() {
		if embedCh != nil {
			close(embedCh)
			<-embedDone
			embedCh = nil
		}
	}

	// Phase 2: the per-document fragmenter choice.
	window, stride, floor := ResolveFragParams(fc.Window, fc.Stride, fc.Floor, fc.EmbedLimit)
	fragMode := fragModeOverlap
	if sawVision && sg != nil {
		fragMode = fragModeLLM
		if err := segmentLLM(ctx, sg, pages, window, sink); err != nil {
			drainEmbed()
			sl.Fail("segment", "llm", err)
			return 0, "", err
		}
		sl.Done("segment", "llm", fmt.Sprintf("%d fragment(s)", len(frags)))
	} else {
		fragmentOverlap(pages, window, stride, floor, sink)
		sl.Done("segment", fragModeOverlap, fmt.Sprintf("%d fragment(s)", len(frags)))
	}

	// Phase 3: drain the embed pipeline, then swap the whole document in.
	var vecs map[int][]float32
	if embedCh != nil {
		close(embedCh)
		res := <-embedDone
		embedCh = nil
		if res.err != nil {
			sl.Fail("embed", "", res.err)
			return 0, "", res.err
		}
		vecs = res.vecs
		sl.Done("embed", "vectors", fmt.Sprintf("%d vector(s)", len(vecs)))
	}

	media := extractMedia(frags, provenance)
	s.embedMedia(ctx, media) // figure embeddings (image-or-description); best-effort
	recipe := fragRecipe(fragMode, window, stride, floor, fc.FigurePrompt)
	if err := s.commitDoc(docPath, title, fragMode, recipe, frags, provenance, media, vecs); err != nil {
		sl.Fail("commit", "", err)
		return 0, "", err
	}
	sl.Done("commit", "", "")
	return len(frags), fragMode, nil
}

// segmentLLM runs the LLM segmenter over the resolved page texts, stitching the
// open fragment across page boundaries (Assembler). Fragments carry no source
// offsets (they are model-emitted text). `page` is the START page, and
// pageSpans records where every later page begins inside the text — without
// them a hit in a stitched fragment resolves to the wrong page.
func segmentLLM(ctx context.Context, sg *Segmenter, pages []resolvedPage, embedLimit int, sink func(stagedFrag)) error {
	a := NewAssembler(func(page, ord int, text string, spans []PageSpan) error {
		sink(stagedFrag{page: page, ord: ord, text: text, pageSpans: spans})
		return nil
	})
	for _, p := range pages {
		open := a.OpenText()
		r, err := sg.SegmentText(ctx, p.text, open)
		if err != nil {
			return err
		}
		// The model is ASKED for 400-800 words and is not bound by the request.
		// Bound it here, before anything downstream has to survive a fragment no
		// embedder will accept.
		r.Fragments = SplitOversized(r.Fragments, embedLimit)
		if err := a.Feed(p.page, r); err != nil {
			return err
		}
	}
	return a.Close()
}

// fragmentOverlap runs the deterministic windower over the document's concatenated
// page text. Pages are joined with pageSep into one source string (so
// get_document reassembles it exactly from the fragments' offsets); each fragment
// is attributed to the page its start offset falls in, and carries its [start,end)
// span into that source.
func fragmentOverlap(pages []resolvedPage, window, stride, floor int, sink func(stagedFrag)) {
	type pspan struct{ page, start int }
	var b strings.Builder
	var spans []pspan
	for i, p := range pages {
		if i > 0 {
			b.WriteString(pageSep)
		}
		spans = append(spans, pspan{page: p.page, start: b.Len()})
		b.WriteString(p.text)
	}
	src := b.String()
	pageForOffset := func(off int) int {
		page := 0
		for _, sp := range spans {
			if off >= sp.start {
				page = sp.page
			} else {
				break
			}
		}
		return page
	}
	// The boundaries that fall INSIDE a fragment, as offsets into its own text.
	//
	// This function already knew where every page began — it computed `spans` to
	// pick a start page and then discarded the rest. An overlap window is sized in
	// characters and pays no attention to page breaks, so most fragments in a
	// multi-page document straddle one; keeping only the start page made the page
	// column wrong for everything after the first break inside each fragment.
	spansWithin := func(start, end int) []PageSpan {
		out := []PageSpan{{Off: 0, Page: pageForOffset(start)}}
		for _, sp := range spans {
			if sp.start > start && sp.start < end {
				out = append(out, PageSpan{Off: sp.start - start, Page: sp.page})
			}
		}
		if len(out) < 2 {
			return nil // wholly on one page; `page` alone is authoritative
		}
		return out
	}
	ordByPage := map[int]int{}
	for _, of := range OverlapFragments(src, window, stride, floor) {
		page := pageForOffset(of.Start)
		ord := ordByPage[page]
		ordByPage[page] = ord + 1
		sink(stagedFrag{page: page, ord: ord, startOff: of.Start, endOff: of.End,
			text: of.Text, pageSpans: spansWithin(of.Start, of.End)})
	}
}

// commitDoc swaps a freshly-built document into the index in ONE transaction:
// upsert the document + its fragmentation recipe, drop its prior
// fragments/vectors/provenance/media (FK cascade drops vectors + media; FTS
// triggers clean the mirror), then insert the new fragments (capturing their ids
// for vectors + media), vectors, page provenance, and media rows. All-or-nothing
// — search never observes a half-updated document.
func (s *Store) commitDoc(docPath, title, fragMode, fragRecipe string, frags []stagedFrag, provenance []stagedPage, media []stagedMedia, vecs map[int][]float32) error {
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	q := gq(tx)         // generated queries bound to this tx

	docID, err := q.UpsertDocument(ctx, gen.UpsertDocumentParams{Path: docPath, Title: title, AddedAt: time.Now().UnixNano()})
	if err != nil {
		return fmt.Errorf("raglit: commit doc: %w", err)
	}
	if err := q.SetDocumentFrag(ctx, gen.SetDocumentFragParams{FragMode: fragMode, FragRecipe: fragRecipe, ID: docID}); err != nil {
		return fmt.Errorf("raglit: set frag mode: %w", err)
	}
	if err := q.DeleteMediaByDoc(ctx, docID); err != nil {
		return err
	}
	if err := q.DeleteFragmentsByDoc(ctx, docID); err != nil {
		return err
	}
	if err := q.DeleteOcrPagesByDoc(ctx, docID); err != nil {
		return err
	}
	fragIDs := make([]int64, len(frags))
	for i, f := range frags {
		fid, err := q.InsertFragment(ctx, gen.InsertFragmentParams{
			DocID: docID, Page: int64(f.page), Ord: int64(f.ord), Text: f.text,
			StartOff: int64(f.startOff), EndOff: int64(f.endOff),
			PageSpans: encodePageSpans(f.pageSpans),
		})
		if err != nil {
			return fmt.Errorf("raglit: insert fragment: %w", err)
		}
		fragIDs[i] = fid
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
	for _, m := range media {
		var fid sql.NullInt64
		if m.fragIdx >= 0 && m.fragIdx < len(fragIDs) {
			fid = sql.NullInt64{Int64: fragIDs[m.fragIdx], Valid: true}
		}
		mediaID, err := q.InsertMedia(ctx, gen.InsertMediaParams{
			DocID: docID, Page: int64(m.page), Ord: int64(m.ord), Kind: m.kind,
			ImagePath: m.imagePath, Bbox: m.bbox, Description: m.description, FragmentID: fid,
		})
		if err != nil {
			return fmt.Errorf("raglit: insert media: %w", err)
		}
		// The figure's embedding (image or description), when one was produced.
		if len(m.vec) > 0 {
			space := m.space
			if space == "" {
				space = "text"
			}
			if err := q.InsertMediaVector(ctx, gen.InsertMediaVectorParams{
				MediaID: mediaID, Dim: int64(len(m.vec)), Vec: encodeVec(m.vec), Space: space,
			}); err != nil {
				return fmt.Errorf("raglit: store media vector: %w", err)
			}
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

// encodePageSpans serialises page boundaries for storage. Empty string when the
// fragment lies on a single page, so the column costs nothing in the common case.
func encodePageSpans(spans []PageSpan) string {
	if len(spans) < 2 {
		return ""
	}
	b, err := json.Marshal(spans)
	if err != nil {
		return ""
	}
	return string(b)
}

// PageAt resolves a byte offset inside a fragment's text to the page it falls
// on. This is the reason the column exists: a search hit is an offset, and
// without the boundaries the only answer available was the fragment's start
// page, which is wrong for everything after the first stitch.
func PageAt(startPage int, spans []PageSpan, off int) int {
	page := startPage
	for _, s := range spans {
		if off < s.Off {
			break
		}
		page = s.Page
	}
	return page
}

// DecodePageSpans parses what encodePageSpans wrote; nil on empty or malformed,
// so a caller degrades to the start page rather than failing.
func DecodePageSpans(s string) []PageSpan {
	if s == "" {
		return nil
	}
	var out []PageSpan
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

// pageImageSHA identifies a page by the bytes the model would see.
func pageImageSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// cachedPageOCR returns a previously transcribed page, if this exact image has
// been read before.
func (s *Store) cachedPageOCR(sha string) (text, engine string, ok bool) {
	row := s.db.QueryRow(`SELECT text, engine FROM ocr_page_cache WHERE img_sha = ?`, sha)
	if err := row.Scan(&text, &engine); err != nil {
		return "", "", false
	}
	return text, engine, true
}

// putPageOCR records a transcribed page so a later failure does not throw it
// away. Best-effort: a cache write must never fail an ingest that succeeded.
func (s *Store) putPageOCR(sha, engine, text string, now int64) {
	_, _ = s.db.Exec(
		`INSERT INTO ocr_page_cache (img_sha, engine, text, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(img_sha) DO UPDATE SET engine = excluded.engine, text = excluded.text`,
		sha, engine, text, now)
}

// ocrPageCached is OCR with the page cache in front of it.
//
// This is what makes a long document survivable. The transcription of a page is
// a pure function of the page image, so a page read once never needs reading
// again — and after a failure the retry resumes at the first page it has not
// already done instead of starting over.
//
// An empty transcription is NOT cached. A page that legitimately has no text is
// indistinguishable here from a page whose OCR returned nothing because the
// upstream was unwell, and caching the second one would make a transient failure
// permanent.
func (s *Store) ocrPageCached(ctx context.Context, ocr *OCR, img PageImage) (text, engine string, cached bool, err error) {
	sha := pageImageSHA(img.Data)
	if t, e, ok := s.cachedPageOCR(sha); ok {
		return t, e, true, nil
	}
	t, e, err := ocr.PageWithEngine(ctx, img)
	if err != nil {
		return "", e, false, err
	}
	if strings.TrimSpace(t) != "" {
		s.putPageOCR(sha, e, t, time.Now().Unix())
	}
	return t, e, false, nil
}
