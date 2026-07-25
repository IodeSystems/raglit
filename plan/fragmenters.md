# raglit fragmenters: deterministic text, the LLM only where it already is

Status: SHIPPED 2026-07-24 (§1,2,4,5 + §3a inline figures + §3b media rows).
§3c and the eval harness are in the icebox. Living doc — prune as slices land.

## Shipped

- **Per-document fragmenter (§1,§3).** `ingestUnits` (pipeline.go) resolves every
  unit to text FIRST, then makes ONE choice: any page escalated to `vision` →
  `llm-seg` (the Assembler path, `segmentLLM`); otherwise `text-overlap` (the
  deterministic windower, `fragmentOverlap`). Text/PDF-text/cheap-OCR pages never
  touch a model. `documents.frag_mode` / `frag_recipe` store the decision; surfaced
  in `list_documents` (`frag_mode`).
- **Deterministic windower (§2).** `fragment.go`: `OverlapFragments(window 9000 /
  stride 6000 / floor 3000, config-overridable, embed-limit-capped)`, boundaries
  snapped to line/paragraph edges, `[start_off,end_off)` on every fragment. Short
  doc → one fragment; sub-floor tail folds (no near-duplicate).
- **Offsets fix get_document (§2).** `reassembleOffsets` (docget.go) reconstructs
  a text-overlap document exactly once from spans; llm-seg/synthetic (0/0) still
  join directly.
- **Embed-model ceiling (§2).** `Embedder.DiscoverEmbedLimit` probes the largest
  accepted input; `Config.EmbedLimitChars` stores it (init wizard step); caps the
  window.
- **Fork removed (§4).** `worker.go` no longer branches on a Segmenter for text;
  `Segmenter.SegmentImage` is deleted; text segmentation is no longer gated on a
  vision model.
- **Figures (§3a,§3b).** The VLM OCR prompt (`figureInstruction`) inlines
  `[FIGURE: …]`; `extractMedia` lifts those into `media` rows anchored to the
  holding fragment, image = whole-page fallback. Media is recomputed on pool reuse,
  not serialized. The born-digital figure GATE (escalate a clean text page that
  carries an image) is opt-in: `OCRConfig.DescribeFigures` (default off), the open
  §6.1 question.
- **Figure embeddings + search (part of §3c).** Each figure gets a `media_vectors`
  row: the IMAGE via an `ImageEmbedder` when one is configured, else the
  DESCRIPTION via the text embedder. A `space` tag records comparability — `text`
  (description), `image-aligned` (image in the text query's joint space), `image`
  (image in a different space, dormant). `Store.SearchFigures` ranks by cosine over
  the query-comparable spaces (`text` + `image-aligned`). Exposed as the
  `search_figures` MCP tool (+ `/search-figures` daemon endpoint) and attached to
  `get_document` (`DocContent.Figures`).
- **nomic-vision image embedder (imageembed.go).** `NewNomicVisionEmbedder`
  (`EmbedImage` over HTTP multipart, `Aligned()=true`) is the shipped
  `ImageEmbedder`. nomic-embed-vision-v1.5 shares nomic-embed-text's space, so an
  image figure is directly comparable to a text query embedded by the (default)
  nomic-text — no separate query tower. Config-driven (`Config.ImageEmbed` +
  `raglit init` step), wired through `Registry.SetImageEmbedder` on daemon / serve /
  index. Requires `embed_model` to be the nomic-text pair for alignment to hold.
  With SigLIP-class models (unaligned) the image vectors are stored but need an
  image-query path — still iceboxed.
- **Recipe (§5).** Pool recipe gains `frag=overlap,w,s,f|fig=<ver>`;
  `documents.frag_recipe` covers fragmentation alone.
- **Cleanup.** The dead LLM-windowing helpers (`WindowCharsFor*`, `textWindows`)
  and the `context_tokens` config/flag/probe are removed — text no longer windows
  for a model. Fragmenter defaults 9000/6000/3000 apply out of the box.

Original design below (kept for rationale).

## Ask (from user)

- A scanned page needs a VLM to become text at all; **while it's there, let it
  fragment too**.
- Text needs no LLM to be fragmented — **overlapping blocks** are enough.
- The fragmenter choice is **per document**.
- **Diagrams must be EXPLAINED into the fragments** so they can be indexed.
  Possibly also: crop figures as media objects, embed the graphics, and record
  their placement in a fragment/page.
- Specialized fragmenters (markdown, code) come later.

## 1. The rule: not a file type, a "is a model already in the loop"

The OCR cascade is cheap engine → gibberish gate → VLM (`ocr.go:49-65`). A clean
scanned page returns `tesseract`/`paddleocr` and **never touches an LLM**. So
"the model is already there" is narrower than "this is a PDF": it means a page
escalated to `vision`.

**Decided (USER, 2026-07-24): per DOCUMENT.** A document with ≥1 page
transcribed by `vision` is LLM-segmented as a whole; every other document is
fragmented deterministically. Per-page would feed two different fragmenters into
one `Assembler`, whose continuation stitching goes incoherent exactly where the
rule changes. Cost is one pass over the page engines before segmenting, and
`pipeline.go` already tallies them in `ocrEngines`.

Consequence: `pipeline.go`'s per-unit `SegmentText` call becomes a per-document
choice made BEFORE the unit loop.

### The mode is a DOCUMENT property, and gets stored as one

Today `mode` lives on `ingest_jobs` (`sql/schema.sql:45`) with values
`llm` / `offline` / `pooled` / `unchanged` (`worker.go:140-241`). That is the
wrong home for this question, twice over:

- A job is one ATTEMPT; the document persists. "How was this document
  fragmented, with what parameters" outlives every job row.
- `pooled` and `unchanged` describe the job's OUTCOME, not a fragmenter. A pool
  hit reuses fragments built by whatever recipe produced the pool entry, so
  `job.mode` literally cannot answer the question.

The per-document rule above is what makes a stored answer well-defined — under
per-page fragmenting the field would have been meaningless. So `documents`
gains:

- `frag_mode TEXT` — `text-overlap` | `llm-seg`.
- `frag_recipe TEXT` — a hash of ONLY the fragmentation inputs (mode, window,
  overlap, figure-prompt version). Deliberately narrower than the pool recipe
  (§5), which also mixes in the embed model and OCR engine; this one answers
  "which documents need re-fragmenting after a stride change" without dragging
  in every unrelated model swap.

The deciding signal is already computed per document — `list_documents` returns
a `vision` page count today (`cmd/raglit/serve.go:368`, `httpd.go:570`, derived
from `ocr_pages.engine`). Materialize the DECISION rather than re-deriving it
per query, and surface `frag_mode` alongside that count.

## 2. Deterministic text fragmenter (the default path)

Overlapping windows: fixed size, stride < window, boundaries snapped to
line/paragraph edges so a fragment never opens mid-sentence.

- **Replaces `TextFragments`** (blank-line split, no size floor — `worker.go:268`)
  and becomes the path for ALL text/code regardless of configured models. The
  fork on `w.Segmenter != nil` (`worker.go:234`) goes away.
- **Why overlap:** it structurally solves what `segment.go:185-190` worries
  about — a hit below the size floor loses its surrounding context — instead of
  paying a model to judge boundaries.
- **Its own assembler, not the existing one.** `Assembler` defers an open
  fragment and stitches cross-unit spans because the MODEL picks boundaries; a
  windower needs neither. Two small fragmenters beat one parameterized one.
- **Costs, accepted:** index and embedding volume grow by the overlap fraction
  (cheap next to LLM generation), and one document can return two near-duplicate
  hits covering the same text. Either dedup same-doc overlapping hits at query
  time or let ranking absorb it — decide when measured, not now.
- **Overlap is TUNABLE, and the right value is unknown** (USER, 2026-07-24).
  Window, stride, and floor are config, not constants; no default is committed
  here on taste. They are inputs to `frag_recipe` (§1), so changing one marks
  the affected documents for reprocessing (§4a).
- **Degenerate inputs:** a document shorter than one window is one fragment with
  no overlap; define the floor before the stride so a barely-over document does
  not emit two near-identical windows.

### Fragments carry source offsets

`start_off` / `end_off` into the source text, on every fragment. Required, not
ornamental:

- **It fixes `get_document`.** Reassembly joins fragment texts in page/ord order
  (`docget.go:120-124`); overlapping fragments share text by construction, so
  without offsets the returned blob repeats every overlap region. With them the
  reader reconstructs the document once and exactly.
- Same-doc overlapping HITS become dedupable by span rather than by guessing.
- Citations and highlight ranges become exact.
- Media bboxes (§3b) get something precise to anchor to.

Offsets are 0/absent on the `llm-seg` path, where a fragment is model-emitted
text and not a span of anything. Page attribution stays "the page the fragment
STARTED on" — with the span recorded, a single `page` column loses nothing.

### The ceiling is bounded by the EMBED model, not by taste

`EmbedDocs` sends fragment text straight to the endpoint with no client-side
length check (`embed.go:49-62`). A 9000-char fragment is ~2000+ tokens and many
embedding models take far less; what a given endpoint does past its limit
(error, truncate, accept) is endpoint-specific and NOT something raglit should
find out the hard way per fragment.

**Decided (USER, 2026-07-24): probe the limit once and store it**, the same
shape as the existing context discovery (`llm.DiscoverContext`, and
`Config.ContextTokens` / `WindowCharsForHome` in `window.go`) — iterate to find
the largest accepted input, keep it in config, and cap the fragment ceiling by
it. Model capabilities are tracked, not assumed.

## 3. Figures: explain them into the fragment

Two tiers. (a) is cheap and captures most of the retrieval value; (b) is the
media model; (c) is deferred.

**(a) Inline description — no schema change.** The VLM OCR prompt
(`defaultOCRPrompt`, `ocr.go`) also describes figures inline and marked, e.g.
`[FIGURE: sequence diagram — client → gateway → auth service; note "retry 3x"]`.
It lands in the page text, flows into fragments unchanged, and indexes in FTS +
text vectors through the existing path. A described diagram is searchable as
text — no new infrastructure at all.

- **This adds a SECOND reason to escalate to the VLM.** A page whose cheap OCR
  text is clean never reaches the model today, so its diagram is never
  described. The gibberish gate judges TEXT QUALITY; a figure gate judges
  VISUAL CONTENT. They are orthogonal.
- Detection: born-digital PDFs expose image XObjects through **pdfcpu, already a
  dependency**. Rasterized/scanned pages need a heuristic (ink density, large
  non-text regions) or unconditional escalation for image-bearing documents.
  **Open** — cheapest first cut is "escalate when pdfcpu reports an embedded
  image on the page".

**(b) Media objects — schema + storage.** Crop each figure to its own image
beside the page images (`savePageImage` already owns the `pages/` dir and a
deterministic path), and record where it belongs:

```
media(id, doc_id, page, ord, kind, image_path, bbox, description, fragment_id)
```

Anchored to the FRAGMENT so a search hit can carry its figures. Written in the
same atomic swap as fragments (`commitDoc`), which is also the only point where
fragment ids exist.

- bbox source: born-digital → pdfcpu XObject placement (deterministic, free).
  Scanned → ask the VLM for approximate regions (small models are unreliable at
  pixel precision; "roughly where" may be enough), else fall back to the WHOLE
  PAGE image, which raglit already saves. A figure whose crop is the page still
  beats no media object.
- **Lifecycle:** deterministic crop paths so writes are idempotent (as
  `savePageImage` already does for pages), and GC for crops orphaned when a
  document is reprocessed into different regions.

**Placement: the description goes INLINE; the crop anchors to the fragment that
contains it.** A figure does not become its own fragment — a lone caption is
almost always under the size floor, and starving it of surrounding text is the
exact failure the floor exists to prevent. So the description rides the
surrounding prose (tier a), and a media row (tier b) points at the fragment
holding its description. A search hit therefore arrives with its figures
attached, which satisfies both "explained in the fragments" and "placement in a
fragment/page" without a starved standalone fragment.

**(c) Image embeddings — deferred, and it does not fit the current store.**
`fragment_vectors` is `PRIMARY KEY(fragment_id)` with no model/space column
(`sql/schema.sql:33`), so a fragment holds exactly one vector in one space. Text
embeddings and CLIP-style image embeddings are DIFFERENT SPACES — cosine across
them is meaningless. Either a separate table per space fused with the RRF search
already uses across indexes, or a shared multimodal space whose text tower
embeds the query. → icebox.

## 4. Consequences already settled

- `worker.go:234` fork removed — text never asks for a model.
- Text segmentation stops being gated on `visionModel` (`queuecmd.go:57-61`);
  that coupling dissolves.
- `Segmenter.SegmentImage` (`segment.go:82`) is dead today (the pipeline OCRs
  first, then always calls `SegmentText`) and stays dead under this rule →
  delete it.

### 4a. Re-fragmenting is not a thing; REPROCESSING is

**Decided (USER, 2026-07-24): a stale document re-runs the WHOLE pipeline** —
extract, OCR, fragment, embed, commit. No re-fragment-only path, no separately
cached OCR-text layer to keep coherent. `frag_recipe` identifies WHICH documents
are stale; reprocessing them is the ordinary ingest path, not a special one.

- **Atomic swap is already the guarantee** (USER flagged it; it holds today).
  `ingestUnits` builds the entire new document in memory and `commitDoc` swaps
  fragments + vectors + provenance in ONE transaction (`pipeline.go:18-25`,
  `179-215`), so a failed reprocess leaves the prior version intact rather than a
  torn one. Media rows (§3b) join that transaction.
- **Known cost, accepted:** a stride change re-OCRs the vision-transcribed
  documents, because the fragmenter params are in the pool recipe and a recipe
  miss reprocesses from source. Text documents — the common case — cost nothing
  to redo. An OCR-text cache layer would avoid it and stays available later; it
  is NOT needed for correctness and is not being built now.
- **Versioned fragments for A/B** (keep old fragmentations side by side to
  compare) — considered and explicitly NOT wanted yet (USER). → icebox.
- **Migration on start.** raglit already applies additive migrations on every
  index open (`store.go:106-112` → `migrate`, `ALTER TABLE ADD COLUMN`), and the
  daemon opens indexes through that path, so `frag_mode` / `frag_recipe` /
  offsets land on existing indexes with no rebuild. Add each new column to BOTH
  `sql/schema.sql` and the `migrate` list.

## 5. The recipe hash must cover the fragmenter

`queuecmd.go:74` builds `seg=%s|emb=%s|ocr=%s|win=%d`. It has to gain the
fragmenter MODE, window, overlap, and the figure-prompt version. The pool is
keyed by `(recipe_hash, file_hash)` — without this, tuning the stride silently
serves fragments built by the old one.

Two hashes, on purpose: the POOL recipe (everything that shapes the cached
artifact — fragmenter + embed model + OCR engine) decides whether cached work is
reusable; `documents.frag_recipe` (§1) covers fragmentation alone and decides
which documents need re-fragmenting. Deriving the second as a component of the
first keeps them from drifting.

## 6. Open questions

1. **Figure-gate detection for scanned (non-born-digital) pages.** STILL OPEN.
   Born-digital is wired (pdfcpu XObjects → `pagesWithImages`) behind opt-in
   `OCRConfig.DescribeFigures`; a raster page needs a heuristic (ink density /
   large non-text regions) or a blanket rule. The opt-in flag ships the mechanism
   dormant so the default frag_mode distribution is unchanged.
2. **Window / stride / floor defaults.** RESOLVED for now: 9000 / 6000 / 3000
   (USER, 2026-07-24), inheriting the old floor/ceiling. Config-overridable, capped
   by the embed limit. Move with measurement once an eval harness exists.
3. **Same-doc overlapping-hit dedup:** DECIDED — leave it to ranking (USER,
   2026-07-24). Offsets make a span-dedup exact if this is revisited.

Resolved since the first draft: `get_document` duplication (offsets, §2), the
embed-model ceiling (probe + store, §2), page attribution (start page, §2),
figure placement (inline + anchored crop, §3), re-fragmentation (full
reprocess, §4a), media lifecycle (§3b), degenerate inputs (§2), migrations
(§4a).

## 7. Not doing yet (→ icebox)

- Specialized fragmenters: markdown headings with breadcrumb prefixes; code via
  poly-lsp `symbols.FileSymbols` (symbol path + class + doc-comment span +
  `BodyStartLine` as atoms), once poly-lsp has a daemon to call.
- Image embeddings / a second vector space.
- **An eval harness.** None of this is provable without a fixed query set with
  known-relevant documents and a recall@k score. Until that exists, fragmenter
  tuning is taste.
