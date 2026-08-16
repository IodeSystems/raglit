package raglit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	// dpi is the resolution this page was rendered at, carried through to the
	// page's provenance: it decides what any reader could possibly see, and
	// without it "why is this page wrong" cannot be answered afterwards.
	dpi int
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
	// origin marks text nobody wrote: "described" for a model's account of an
	// image. Empty for transcription, which is the overwhelming majority and the
	// only kind that may be quoted as the record. See FragOriginDescribed.
	origin string
}

// stagedPage is a page's provenance held in memory until the atomic swap.
type stagedPage struct {
	page    int
	engine  string
	model   string
	dpi     int
	imgPath string
	// How much of this page the model DESCRIBED rather than transcribed, in
	// characters of indexable text. Measured here because this is the only moment
	// it can be: the evidence is the layout markup, and it is stripped a few lines
	// later. See DescribedFraction.
	//
	// Two counts rather than a percentage, so a document's fraction is the exact
	// weighted total rather than an average of averages — a 40-character caption
	// page must not count as much as a 4,000-character one.
	textChars      int
	describedChars int
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
	// SegmentInputLimit caps ONE segmentation request — prompt, carried-over
	// fragment and page text together — at what the chat endpoint accepts, IN
	// TOKENS, which is the unit the endpoint counts in. Distinct from EmbedLimit,
	// which bounds the fragments that come BACK, in characters, because that
	// limit belongs to the embedder. Measuring only the second is how a page too
	// big to send reached the endpoint and failed the document. 0 → unknown, no
	// cap.
	SegmentInputLimit int
	// TokenCounter counts tokens as the serving model does, when the endpoint can
	// (llm.Client.CountTokens). nil → sizes are estimated from a ratio learned
	// per document instead. The difference is not cosmetic: measured with the
	// model's own tokenizer, this corpus runs 4.66 characters per token on prose
	// and 1.16 on a survey legal description.
	TokenCounter TokenCounter
	// EmbedLimitTokens and EmbedTokenCounter are the same pair for the EMBEDDER,
	// and they are a separate model with a separate tokenizer — nomic-embed-text
	// answers in BERT ids where the chat model answers in its own.
	//
	// This is the limit that actually refused documents. EmbedLimit above is a
	// character number converted from the embedder's 8192-token context at an
	// assumed two characters per token, i.e. 16128 — and a scanned court brief
	// reaches 10240 tokens inside that, so the fragment was allowed, sent, and
	// refused with a 500 that failed the whole document at the embed stage.
	// Characters cannot express this limit; only tokens can.
	EmbedLimitTokens  int
	EmbedTokenCounter TokenCounter
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

// resolvedPage is one unit's text after OCR, plus its page number and WHO read
// it — the engine, and the model behind it where there is one.
type resolvedPage struct {
	page   int
	text   string
	engine string
	model  string
}

// pageFailure is one page that could not be read, kept so the document can be
// committed WITHOUT it while still saying which page is missing. A hole that
// names itself can be re-read; a document that silently lost a third of itself
// cannot even be noticed.
type pageFailure struct {
	page int
	err  error
}

// anyPageRead reports whether at least one page produced text. The threshold for
// committing a partial document: some content beats none, none is a failure.
func anyPageRead(pages []resolvedPage) bool {
	for _, p := range pages {
		if strings.TrimSpace(p.text) != "" {
			return true
		}
	}
	return false
}

// failedPageList renders the missing page numbers for a stage line, capped so a
// wholly-unreadable scan does not print two hundred numbers.
func failedPageList(f []pageFailure) string {
	const max = 12
	var b strings.Builder
	for i, pf := range f {
		if i == max {
			fmt.Fprintf(&b, " +%d more", len(f)-max)
			break
		}
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "p%d", pf.page)
	}
	return b.String()
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
	var failedPages []pageFailure
	ocrEngines := map[string]int{}
	cachedHits := 0
	sawVision := false
	for _, u := range units {
		text := u.text
		// The text layer, when it is used at all, is a page NOBODY read — see
		// pdfUnits. Named so, because the one thing worse than an unread page is
		// an unread page that looks read.
		engine, model := "text-layer", ""
		if u.isImage() {
			if ocr == nil {
				sl.Fail("ocr", "", fmt.Errorf("page %d is an image but no OCR is configured", u.page))
				return 0, "", fmt.Errorf("page %d is an image but no OCR is configured", u.page)
			}
			t, eng, fromCache, err := s.ocrPageCached(ctx, ocr, PageImage{Page: u.page, Mime: u.mime, Data: u.data})
			if err != nil {
				// SALVAGE. This used to return and lose the WHOLE document, the
				// same defect unitsToPageText was fixed for on 2026-08-06 — the
				// fix never reached the ingest path, so the daemon kept doing it.
				// Measured 2026-08-09: a 5-page lot certification and a 9-page
				// billing narrative were indexed as NOTHING because one page each
				// tripped the repetition guard on a dense table.
				//
				// A page failing is a fact about that page. Record it, keep the
				// rest, and let the hole name itself; only a document where
				// nothing read at all is a failed document. The page image is
				// still saved so the hole can be re-read without re-fetching.
				//
				// Pages already transcribed on this run stay in the cache, so a
				// later re-read resumes rather than starting the document again.
				sl.Fail("ocr", eng, fmt.Errorf("page %d: %w (%d page(s) already cached and will not be re-read)", u.page, err, cachedHits))
				failedPages = append(failedPages, pageFailure{page: u.page, err: err})
				imgPath := ""
				if p, e := s.savePageImage(docPath, u.page, u.mime, u.data); e == nil {
					imgPath = p
				}
				provenance = append(provenance, stagedPage{page: u.page, engine: "failed", model: ocr.Model, dpi: u.dpi, imgPath: imgPath})
				pages = append(pages, resolvedPage{page: u.page, engine: "failed"})
				continue
			}
			if fromCache {
				cachedHits++
			}
			text = t
			engine = eng
			if isVisionEngine(engine) {
				model = ocr.Model
			} else {
				model = engine
			}
			ocrEngines[engine]++
			if isVisionEngine(engine) {
				sawVision = true
			}
			// Save the page image + record provenance with the REAL cascade engine
			// (tesseract/paddleocr/vision), so review shows which pages escalated.
			imgPath := ""
			if p, e := s.savePageImage(docPath, u.page, u.mime, u.data); e == nil {
				imgPath = p
			}
			provenance = append(provenance, stagedPage{page: u.page, engine: engine, model: model, dpi: u.dpi, imgPath: imgPath})
		} else if u.page >= 1 {
			provenance = append(provenance, stagedPage{page: u.page, engine: engine})
		}
		pages = append(pages, resolvedPage{page: u.page, text: text, engine: engine, model: model})
	}
	// Every page a hole is a failed document; some holes among readable pages is a
	// partial one, which is worth keeping. Reported either way, because an ingest
	// that quietly drops a third of a document is the failure this guards.
	if len(failedPages) > 0 {
		if !anyPageRead(pages) {
			sl.Fail("ocr", "", fmt.Errorf("every page failed to read: %w", failedPages[0].err))
			return 0, "", fmt.Errorf("raglit: %s: every page failed to read: %w", docPath, failedPages[0].err)
		}
		sl.Done("ocr", "", fmt.Sprintf("SALVAGED: %d of %d page(s) unread (%s) — the rest were indexed",
			len(failedPages), len(units), failedPageList(failedPages)))
	}

	// NO TRANSCRIPTION SIDECAR IS WRITTEN HERE, and this is deliberate.
	//
	// Ingest used to materialise <doc>.raglit-transcription.md beside every
	// document it read. It put 407 files into one corpus — a legal evidence tree
	// — and every one of them duplicated text raglit already had: the same paged
	// reading is in `pages`, in the fragments, and served by /api/doc-detail,
	// which is what the Transcript tab renders.
	//
	// The cost was not just the files. raglit then had to defend itself against
	// them: IsGeneratedSidecar refuses to ingest one, builtinIgnore hides them
	// from discovery, and WriteTranscriptionCorrected refuses to transcribe a
	// transcription — four mechanisms guarding an indexer against its own
	// output. A feature raglit owns belongs in raglit's storage, not scattered
	// through somebody's corpus for raglit to trip over later.
	//
	// `raglit transcribe` still writes one, because that is a person asking for
	// a file. The difference is who decided.

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

	// FROM HERE ON, `pages` is INDEX text, not transcription text.
	//
	// Everything above needed the model's own words with its layout markup — the
	// transcription artifact keeps the bounding boxes, because that is how a
	// person checks a quotation against the pixels and how a region is located.
	// Everything below is the index, which wants none of it: `data-bbox`
	// coordinates embed as if they were words, attributes eat fragment budget,
	// and the SEGMENTER is handed twice the text it needs.
	//
	// Measured on the delano corpus 2026-08-10: 413 of 2692 fragments carried this
	// markup and 40% of their bytes were tags — one 1947 deed was 49% tags, and
	// its segmentation failed with `unexpected end of JSON input`, the reply
	// truncated mid-object. Both a chandra and a Qwen segmenter failed on it; the
	// input was the fault, not either model.
	//
	// Deliberately AFTER the writeback and BEFORE segmentation: those two want
	// different text, and conflating them is what left this unwired.
	flattened := 0
	// Which pages the model DESCRIBED rather than transcribed, decided here
	// because this is the last moment the evidence exists: the labels that say so
	// are layout markup, and the next three lines delete them. See
	// DescribedFraction.
	describedPages := map[int]bool{}
	// And the GRADED measure alongside the binary one, kept per page.
	//
	// A page is not one or the other. A SCREENSHOT transcribes the messages and
	// narrates the phone around them — status bar, app icons, microphone and
	// camera buttons — and lands around 40%: too little to mark as a description,
	// far too much to call a clean transcription. The binary test answers the
	// binary question (may this be quoted as the record?) and must stay binary;
	// this answers "how much of it did a model make up?", which a reader deciding
	// how far to trust the page needs and nothing recorded.
	measured := map[int][2]int{}
	for i := range pages {
		if IsDescribedPage(pages[i].text) {
			describedPages[pages[i].page] = true
		}
		// Only a page that CAN be scored is scored. A model that was never asked to
		// mark what it described returns transcription and description run
		// together with no seam, and calling that 0% is a claim, not an absence —
		// see DescribableRead. Anything that is not a model read (a text layer, a
		// glyph recogniser) cannot describe at all, so 0 there is true.
		scorable := !isVisionEngine(pages[i].engine) || DescribableRead(pages[i].text)
		flat := FlattenForIndex(pages[i].text)
		if n := len(flat); n > 0 && scorable {
			d := measured[pages[i].page]
			measured[pages[i].page] = [2]int{
				d[0] + n,
				d[1] + int(DescribedFraction(pages[i].text)*float64(n)+0.5),
			}
		}
		if flat != pages[i].text {
			flattened += len(pages[i].text) - len(flat)
			pages[i].text = flat
		}
	}
	for i := range provenance {
		if m, ok := measured[provenance[i].page]; ok {
			provenance[i].textChars, provenance[i].describedChars = m[0], m[1]
		}
	}
	markDescribed := func(f *stagedFrag) {
		if len(describedPages) == 0 {
			return
		}
		// Every page the fragment touches must be described. A fragment that
		// mixes a photograph's caption with transcribed text is mostly real, and
		// calling the whole of it generated would misrepresent the real half —
		// which is its own kind of lie. Under-marking there is the safer error
		// and it is stated rather than hidden.
		pages := []int{f.page}
		for _, sp := range f.pageSpans {
			pages = append(pages, sp.Page)
		}
		for _, p := range pages {
			if !describedPages[p] {
				return
			}
		}
		f.origin = FragOriginDescribed
	}
	if len(describedPages) > 0 {
		sl.Done("described", "", fmt.Sprintf(
			"%d page(s) are the model's DESCRIPTION of an image, not a transcription — "+
				"indexed so they are findable, marked so they are not quoted as the record",
			len(describedPages)))
	}
	if flattened > 0 {
		sl.Done("flatten", "", fmt.Sprintf("removed %d bytes of layout markup before segmenting", flattened))
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
	// The embedder's ceiling, enforced HERE because this is the only point every
	// fragment passes through on every path.
	//
	// It used to be enforced twice and in the wrong unit: SplitOversized bounded
	// the llm-seg path in characters, and the deterministic windower bounded
	// itself by a window that was the same character number. Neither is what the
	// embedder counts. A document that fell back from llm-seg to the windower —
	// which is exactly what a hard document does — took the path with no
	// token-level check at all, and its first dense fragment failed the whole
	// ingest at the embed stage with "input (10240 tokens) is too large".
	embedBudget := NewTokenBudget(ctx, fc.EmbedTokenCounter, fc.EmbedLimitTokens)
	// What the EMBEDDER adds to a fragment before the endpoint sees it. Counting
	// the fragment alone was off by exactly the difference: a fragment sized to
	// 8192 came back "input (8194 tokens) is too large to process".
	if s.embedder != nil {
		embedBudget.Overhead = s.embedder.DocPrefix
	}
	embedBudget.Reserve = embedSpecialReserve
	var frags []stagedFrag
	ordByPage := map[int]int{}
	sink := func(f stagedFrag) {
		for _, piece := range splitForEmbed(ctx, embedBudget, f) {
			// Renumbered because a split makes the producer's ord wrong: ord locates
			// a fragment within its page, and two pieces cannot share one.
			piece.ord = ordByPage[piece.page]
			ordByPage[piece.page]++
			// Marked HERE, at the one point every fragment on every path passes
			// through, rather than in each producer — the llm-seg and
			// text-overlap paths both feed this, and a mark applied in one of
			// them would silently not apply to documents that took the other.
			markDescribed(&piece)
			idx := len(frags)
			frags = append(frags, piece)
			if embedCh != nil {
				embedCh <- embedItem{idx: idx, text: piece.text}
			}
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
		// One budget per document: what it measures about this document's density
		// is what makes the next unit's estimate right, and a survey sheet and a
		// brief have no business informing each other's.
		budget := NewTokenBudget(ctx, fc.TokenCounter, fc.SegmentInputLimit)
		// A per-document copy, so the embedder's budget can be attached without
		// mutating a Segmenter the worker reuses across jobs.
		sgDoc := *sg
		sgDoc.FragBudget = embedBudget
		rep, err := segmentLLM(ctx, &sgDoc, pages, embedBudget, budget, sink)
		var short *ErrSegmentShort
		switch {
		case errors.As(err, &short):
			// The model did not account for what it was given, so this document
			// falls back to the deterministic windower — WHOLE, not per page.
			//
			// Silently indexing most of a document is the worst outcome
			// available: there is no error, the page count looks right, and the
			// missing text is discovered only when somebody searches for
			// something that is provably in the file. Measured on a 2-page record
			// of survey: page 2 held the EXISTING CORNERS table — every found
			// monument and its offset from calculated position — 2,072 characters
			// that reached OCR, reached the transcription sidecar, and produced
			// no fragment at all. It was invisible to search for as long as it
			// was indexed.
			//
			// Whole-document rather than a mixed run because frag_mode and
			// frag_recipe describe the document, and half of each is not a
			// recipe anybody can reason about later.
			frags = frags[:0]
			fragMode = fragModeOverlap
			fragmentOverlap(pages, window, stride, floor, sink)
			sl.Done("segment", fragModeOverlap,
				fmt.Sprintf("%d fragment(s) — llm-seg dropped text, fell back (%s)", len(frags), short.Detail()))
		case err != nil:
			drainEmbed()
			sl.Fail("segment", "llm", fmt.Errorf("%w (%d request(s) over %d page(s))", err, rep.requests, rep.pageCount))
			return 0, "", err
		default:
			// What the stage DID, not only what it produced. A stage that made one
			// request per page and one that made thirty are different stages, and
			// they used to record the same line — which is how an hour spent in
			// segmentation was reported as a single call's error.
			detail := fmt.Sprintf("%d fragment(s), %d request(s) over %d page(s)",
				len(frags), rep.requests, rep.pageCount)
			if rep.subunits > 0 {
				detail += fmt.Sprintf(", %d sub-unit(s) to fit the input limit", rep.subunits)
			}
			if d := rep.degradedDetail(); d != "" {
				// Not an error — the pages are indexed and searchable — but not a
				// clean segmentation either, and a "done" that cannot tell them
				// apart is how this stayed invisible.
				sl.Record("segment", "llm", "degraded", detail+" — "+d)
			} else {
				sl.Done("segment", "llm", detail)
			}
		}
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
	if err := s.commitDoc(docPath, title, fragMode, recipe, frags, provenance, media, vecs, nil); err != nil {
		sl.Fail("commit", "", err)
		return 0, "", err
	}
	sl.Done("commit", "", "")
	// What each page was read to say, and BY WHAT — recorded after the commit,
	// so a failed ingest leaves no readings behind.
	//
	// Every read, not only the corrected ones. Until now a machine transcription
	// existed only as fragments, so "what did the model say this page was" could
	// not be asked once the text had been segmented, and a re-read by a different
	// model overwrote the answer with no record that it had changed. A person's
	// correction is never unseated by this — see AddPageReading.
	s.recordPageReadings(ctx, docPath, pages)
	return len(frags), fragMode, nil
}

// Identity is not produced here, and that is the point.
//
// A caption is downstream OF the transcript: there is nothing to summarise until
// the text exists, and a document whose OCR produced nothing cannot be named no
// matter how many times it is asked. Ingest therefore does one thing about it —
// records that the work is due, in the same transaction that commits the text
// (see commitDoc) — and the captioning queue does the rest at the endpoint's
// concurrency.
//
// What that buys is the edge firing on its own. When a document is re-read and
// its transcript changes, the caption is re-queued by the commit itself: the
// lead-based-paint disclosure that was indexed as its own signature stamp had
// nothing to caption, was skipped, and the moment a real transcript replaced the
// overlay the job came back without anybody remembering to ask. An inline call
// could not do that — it runs once, at a moment when the answer may be "there is
// nothing here yet", and nothing revisits it.

// textHashOf fingerprints the document's own words as committed — the same
// measure IdentityTextHash applies to the text a caption is written from, so the
// two are comparable. The identity fragment is not among these: a caption is not
// part of the text it describes.
func textHashOf(frags []stagedFrag) string {
	parts := make([]string, 0, len(frags))
	for _, f := range frags {
		parts = append(parts, f.text)
	}
	return IdentityTextHash(strings.Join(parts, pageSep))
}

// recordPageReadings stores this run's transcription of each page, attributed to
// the engine and model that produced it. Best-effort: the document is indexed
// either way, and a missing provenance row is worth less than a failed ingest.
func (s *Store) recordPageReadings(ctx context.Context, docPath string, pages []resolvedPage) {
	if docPath == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, p := range pages {
		if p.page < 1 || strings.TrimSpace(p.text) == "" {
			continue
		}
		_ = s.AddPageReading(ctx, PageReading{
			Doc: docPath, Page: p.page, Text: p.text,
			Source: "machine", Engine: p.engine, Model: p.model, At: now,
		})
	}
}

// segmentLLM runs the LLM segmenter over the resolved page texts, stitching the
// open fragment across page boundaries (Assembler). Fragments carry no source
// offsets (they are model-emitted text). `page` is the START page, and
// pageSpans records where every later page begins inside the text — without
// them a hit in a stitched fragment resolves to the wrong page.
func segmentLLM(ctx context.Context, sg *Segmenter, pages []resolvedPage, embedBudget, budget *TokenBudget, sink func(stagedFrag)) (segReport, error) {
	return segmentLLMWith(ctx, sg.SegmentText, pages, embedBudget, budget, sink)
}

// segReport is what segmentation did, as opposed to what it returned.
//
// Requests, because a stage that made thirty calls and a stage that made one are
// not the same stage and used to record identically. Degradations, because a
// page the model would not segment comes back as a valid result and is otherwise
// indistinguishable from a page it segmented into one fragment.
type segReport struct {
	requests  int
	degraded  []segDegradation
	subunits  int // requests beyond one per page: the input cap doing its work
	pageCount int
}

type segDegradation struct {
	page   int
	reason string
}

// String renders the degradations for a stage row: the pages, and the reason
// they share when they share one.
func (r segReport) degradedDetail() string {
	if len(r.degraded) == 0 {
		return ""
	}
	pages := make([]string, 0, len(r.degraded))
	sameReason := r.degraded[0].reason
	for _, d := range r.degraded {
		pages = append(pages, strconv.Itoa(d.page))
		if d.reason != sameReason {
			sameReason = ""
		}
	}
	s := fmt.Sprintf("%d page(s) not segmented, returned whole: %s", len(r.degraded), strings.Join(pages, ","))
	if sameReason != "" {
		s += " — " + sameReason
	}
	return s
}

// segmentLLMWith is segmentLLM over any segmenting function.
//
// The seam exists for the coverage rule below, which is the part that has to be
// tested and the part a live model cannot test: to prove the alarm fires when a
// page is dropped, something has to drop a page on purpose.
func segmentLLMWith(ctx context.Context, segment func(context.Context, string, string) (SegResult, error), pages []resolvedPage, embedBudget, budget *TokenBudget, sink func(stagedFrag)) (segReport, error) {
	// Emitted content per page, against what that page was given. The segmenter
	// is a MODEL, and a model that returns a short answer returns it without an
	// error — so the only way to know it dropped the back half of a page is to
	// count.
	emitted := map[int]int{}
	rep := segReport{pageCount: len(pages)}
	a := NewAssembler(func(page, ord int, text string, spans []PageSpan) error {
		emitted[page] += contentChars(text)
		sink(stagedFrag{page: page, ord: ord, text: text, pageSpans: spans})
		return nil
	})
	for _, p := range pages {
		// A page is not automatically a request the endpoint will accept. Cut it
		// into as many sub-units as the input limit requires — the assembler
		// stitches consecutive Feeds of the same page exactly as it stitches
		// across a page boundary, so sub-units cost nothing but calls.
		perPage := 0
		for rest := p.text; ; {
			open := segmentOpenForRequest(ctx, a.OpenText(), budget)
			take := budget.Fit(ctx, segPrompt(open)+segContentHeader, rest, segmentMinTake)
			r, err := segment(ctx, rest[:take], open)
			rep.requests++
			perPage++
			if err != nil {
				return rep, err
			}
			// A unit the model would not segment comes back as one whole-unit
			// fragment and no error. Record it here, where the page is known.
			if r.Degraded != "" {
				rep.degraded = append(rep.degraded, segDegradation{page: p.page, reason: r.Degraded})
			}
			// The model is ASKED for 400-800 words and is not bound by the request.
			// Bound it here, before anything downstream has to survive a fragment no
			// embedder will accept — and cut at the boundaries the sink would use,
			// so a fragment that reaches both is not cut twice in different places.
			r.Fragments = SplitOversized(ctx, embedBudget, r.Fragments)
			if err := a.Feed(p.page, r); err != nil {
				return rep, err
			}
			rest = rest[take:]
			if rest == "" {
				break
			}
		}
		rep.subunits += perPage - 1
	}
	if err := a.Close(); err != nil {
		return rep, err
	}

	// Coverage, checked after Close so the final open fragment is counted.
	//
	// Compared on CONTENT characters — letters and digits — because the model
	// legitimately reflows whitespace, drops page furniture and joins hyphenated
	// line breaks, and none of that is loss. What is loss is a page whose words
	// are not there.
	//
	// A page is attributed to whichever fragment STARTS on it, so a fragment
	// stitched across a boundary counts entirely toward its start page. That
	// makes per-page accounting approximate, which is why the threshold is
	// deliberately loose: it is a data-loss alarm, not a quality score.
	for _, p := range pages {
		in := contentChars(p.text)
		if in < segmentMinChars {
			continue // too short to say anything about
		}
		if covered := emitted[p.page] + carriedInto(emitted, pages, p.page); covered*100 < in*segmentCoveragePct {
			return rep, &ErrSegmentShort{Page: p.page, In: in, Out: covered}
		}
	}
	return rep, nil
}

// segmentOpenForRequest trims the carried-over fragment to what can be sent
// alongside a useful amount of page.
//
// The open fragment is CONTEXT, not content: it is already held by the assembler
// and will be emitted whatever happens here, and it is in the prompt only so the
// model can say whether the next fragment continues it. On an endpoint whose
// limit is near the fragment ceiling, an untrimmed open fragment plus the
// instructions can consume the entire request and leave no room for the page —
// so every request is refused and the document fails on a page that would fit.
//
// The TAIL is kept, because continuation is decided by how the open fragment
// ENDS. Trimming context to protect content is the only trade available; the
// reverse — cutting the page to preserve its context — loses text.
func segmentOpenForRequest(ctx context.Context, open string, budget *TokenBudget) string {
	if budget.Unlimited() || open == "" {
		return open
	}
	// Room for the instructions, this context, and a floor's worth of page.
	if budget.Tokens(ctx, segPrompt(open)+segContentHeader)+segmentMinTakeTokens <= budget.limit {
		return open
	}
	fixed := budget.Tokens(ctx, segPrompt("")+segContentHeader)
	room := budget.limit - fixed - segmentMinTakeTokens
	if room <= 0 {
		// The instructions alone leave no room. Drop the context entirely rather
		// than send a request that cannot carry a page.
		return ""
	}
	// Fit measures the TAIL, so reverse the question: keep the largest suffix
	// whose token count is inside `room`, found the same way a take is.
	keep := budget.FitSuffix(ctx, open, room)
	return open[len(open)-keep:]
}

// segmentMinTake is the smallest sub-unit worth sending in characters, and the
// guarantee that the take loop always advances.
const segmentMinTake = 1000

// segmentMinTakeTokens is that floor expressed in the unit the limit uses, at
// the worst plausible density (one token per character). Reserving it means a
// request always has room for SOME page, which is what stops the loop.
const segmentMinTakeTokens = segmentMinTake

// carriedInto credits a page whose content was stitched into a fragment that
// started on the previous page. Without it, a page that continues an open
// fragment reads as empty and trips the alarm on a document nothing is wrong
// with.
func carriedInto(emitted map[int]int, pages []resolvedPage, page int) int {
	if emitted[page] > 0 {
		return 0
	}
	for i, p := range pages {
		if p.page == page && i > 0 {
			// The previous page emitted more than its own content: the surplus
			// is this page's, stitched in.
			prev := pages[i-1]
			if extra := emitted[prev.page] - contentChars(prev.text); extra > 0 {
				return extra
			}
		}
	}
	return 0
}

// segmentCoveragePct is how much of a page's content must survive segmentation.
// Loose on purpose: the model reflows, and this exists to catch a page that
// vanished, not one that was tidied.
const segmentCoveragePct = 60

// segmentMinChars is the floor below which coverage says nothing — a page with
// twenty characters on it is noise either way.
const segmentMinChars = 200

// ErrSegmentShort reports that the model-segmenter did not account for a page.
type ErrSegmentShort struct {
	Page    int
	In, Out int
}

func (e *ErrSegmentShort) Error() string {
	return fmt.Sprintf("llm-seg returned %d of %d content characters for page %d", e.Out, e.In, e.Page)
}

// Detail is the short form for a stage log.
func (e *ErrSegmentShort) Detail() string {
	return fmt.Sprintf("page %d: %d of %d chars", e.Page, e.Out, e.In)
}

// contentChars counts letters and digits — the same measure textLayerContent
// uses, and for the same reason: whitespace is not content.
func contentChars(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
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
//
// ident is what this ingest established about the document, or nil for "keep
// what is recorded". Either way the identity FRAGMENT is rewritten here, because
// the fragment wipe above just deleted it: a document whose identity columns
// survive an ingest but whose summary is no longer indexed is exactly the silent
// half-state this transaction exists to make impossible.
func (s *Store) commitDoc(docPath, title, fragMode, fragRecipe string, frags []stagedFrag, provenance []stagedPage, media []stagedMedia, vecs map[int][]float32, ident *DocIdentity) error {
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
			Origin:    f.origin,
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
		if err := q.UpsertOcrPage(ctx, gen.UpsertOcrPageParams{DocID: docID, Page: int64(p.page), Engine: p.engine, ImagePath: p.imgPath,
			TextChars: int64(p.textChars), DescribedChars: int64(p.describedChars)}); err != nil {
			return err
		}
		// The model behind the engine, raw because the generated query predates
		// the column (see TruePages on regenerating the sqlc layer).
		if _, err := tx.ExecContext(ctx, `UPDATE ocr_pages SET model=?, dpi=? WHERE doc_id=? AND page=?`,
			p.model, p.dpi, docID, p.page); err != nil {
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
	id := DocIdentity{}
	if err := tx.QueryRow(
		`SELECT gen_name, gen_summary, gen_kind, gen_source, gen_model, gen_at, gen_text_hash FROM documents WHERE id=?`,
		docID).Scan(&id.Name, &id.Summary, &id.Kind, &id.Source, &id.Model, &id.At, &id.TextHash); err != nil {
		return fmt.Errorf("raglit: read identity: %w", err)
	}
	// A person's caption outranks anything an ingest brings — including a pooled
	// document's, which carries the caption of whichever index cached it first.
	// Enforced here rather than only at the call site because this is the one
	// point every ingest path passes through.
	if ident != nil && !id.ByPerson() {
		id = *ident
	}
	if !id.Empty() {
		// The caption survives the fragment wipe above, and so must the fragment
		// that makes it searchable.
		if err := writeIdentity(ctx, tx, docID, id); err != nil {
			return err
		}
	}
	// Independently of whether one exists: is one OWED? These are separate
	// questions, and folding them into one branch meant a caption could only ever
	// be queued for a document that had none — so a re-read that replaced a
	// document's text wholesale left the old summary in place, describing a
	// transcript that no longer existed.
	if s.identifier != nil && !id.ByPerson() && id.TextHash != textHashOf(frags) {
		// The DAG edge: a caption is downstream of the transcript, so it is owed
		// whenever the transcript it was written from is not the transcript the
		// document now has. That covers both directions — a document with no
		// caption (empty hash, empty caption), and a document whose text was
		// REPLACED under an existing caption, which is what a re-read does: a
		// page that was a signature stamp becomes an agreement, and the summary
		// still describes the stamp.
		//
		// Queued IN THIS TRANSACTION, so "the text changed" and "the caption is
		// owed" cannot come apart. A person's caption is left alone: it is a
		// ruling about the document, not about one machine's reading of it.
		//
		// Only when a model is configured — an index that does not caption should
		// not accumulate work nobody will ever do.
		if err := enqueueIdentityTx(ctx, tx, docPath); err != nil {
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
