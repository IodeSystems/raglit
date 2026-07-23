# raglit OCR: semantic MCP tool + pluggable cascade

Status: design agreed 2026-07-21, not yet built. Living doc — prune as slices land.

## 1. Insight

OCR has **no standard API**. OpenAI has no OCR route; on an OpenAI surface OCR is
just vision chat (`image_url` → text). The one "OCR API" in the toolkit —
ragtag's dropped paddleocr sidecar — was fully bespoke (`POST /ocr`, raw JPEG →
`{text, lines, mean_confidence, box_count}`), not OpenAI at all. So there is
nothing to conform to.

Therefore expose OCR as a **semantic MCP tool** — `document → paged text` — not a
REST endpoint. The tool owns the *intent*; the backend that fulfills it is a
swappable implementation detail. That lets dissimilar providers sit under one
tool: a cheap page-OCR engine (bespoke HTTP or a CLI) AND a vision LLM (OpenAI
`/v1/chat/completions` via corrallm) are interchangeable behind the cascade.
oidio standardizes *one* protocol; this standardizes the *task* and brokers
providers that don't share a protocol.

## 2. What raglit already has (build on, don't rebuild)

- `pagify.go` — `Pagify(pdfPath, outDir) → []PageImage`. raglit already owns
  PDF→page rasterization (pdfcpu). Needs an in-memory sibling for the MCP tool
  (bytes in, no temp files).
- `ocr.go` — `OCR.Page(img) → text`, **VLM-only** via agentkit's multimodal
  `llm.Client` (`Chatter`). No cheap pass, no gate. This is what becomes a cascade.
- `cmd/raglit/serve.go` — a stdio MCP server (`mark3labs/mcp-go`) with tools
  `search` / `ingest` / `index_status` / `list_indexes`. **No OCR tool** — OCR
  only runs as an `ingest` side-effect.
- Config (`config.go`): `BaseURL`, `APIKey`, `VisionModel`, … — the VLM tier
  already points at corrallm (or any OpenAI vision endpoint).

## 3. Decisions (agreed)

1. **raglit replaces ragtag.** The OCR capability lands here; the paddle sidecar
   is not reintroduced as a hard dependency. (Full ragtag retirement is a broader
   migration, out of scope for this slice.)
2. **Docs → paged text, MCP owns rasterization.** The tool takes a document
   (PDF/multi-image) or a single image and returns per-page text. It rasterizes
   internally (`Pagify`), so the caller doesn't.
3. **Cascade: cheap OCR → gibberish gate → VLM fallback.** Most pages are clean
   and must not pay for a 27B VLM per page (this is a corpus-scale indexer). Cheap
   engine first; escalate to the corrallm VLM only when the page looks like
   gibberish.
4. **The cheap tier is pluggable + config-selected:** `none` | `tesseract`
   (exec the CLI, in-process, no container) | `paddleocr` (HTTP to a sidecar the
   user optionally installs, e.g. via docker). Same broker philosophy, one level
   down: the cascade's cheap slot is itself swappable. `none` = today's VLM-only.

## 4. Design

### Cascade (ocr.go)

```
OCR.Page(ctx, img) -> (text, engine):
  if cheap != nil:
    po, err := cheap.OCRPage(ctx, img.jpeg)
    if err == nil:
      if gib, _ := gate.IsGibberish(po); !gib:
        return po.Text, cheap.Name()        // "tesseract" | "paddleocr"
    // err or gibberish → fall through (reason logged for tracing)
  return o.vlmPage(ctx, img), "vision"       // existing VLM path
```

### Pluggable cheap engine (ported from ragtag)

```go
type PageEngine interface {
    OCRPage(ctx context.Context, jpeg []byte) (PageOCR, error)
    Name() string
}
type PageOCR struct { Text string; Lines []OCRLine; MeanConfidence float64; BoxCount int }
```

- **TesseractEngine** — exec `tesseract stdin stdout -l <lang>`; no cgo, no
  daemon. `MeanConfidence`/`Lines` from `tsv` output (or a coarse confidence);
  BoxCount from line count. The footprint-light default.
- **PaddleEngine** — port ragtag's HTTP client verbatim (`POST <url>/ocr`, raw
  JPEG → `{text,lines,mean_confidence,box_count}`). For users who install the
  paddle sidecar for its higher accuracy.
- **nil** — cheap tier disabled; cascade is VLM-only (current behavior).

### Gibberish gate (ported)

ragtag's `internal/extract/gibberish.go` is stdlib-only and drops in unchanged
(rename package): junk-rune fraction, mean-confidence floor, dictionary-free
word-like lexical test. Precision-biased defaults so VLM escalation stays rare.
An empty page (BoxCount 0) is NOT gibberish — emit empty, don't pay the VLM.

### Config additions (config.go)

```
OCR:
  cheap_engine: "none" | "tesseract" | "paddleocr"   # default "none"
  paddle_url:   "http://127.0.0.1:7710"              # when cheap_engine=paddleocr
  tesseract_bin: "tesseract"                         # when cheap_engine=tesseract
  tesseract_lang: "eng"
  gibberish: { … optional GibberishConfig overrides … }
```

### MCP tool surface (serve.go) — the deliverable

New tool `ocr` (a.k.a. `extract`):
- **Input:** `path` (file://… or a local path) OR `data` (base64 bytes) + `mime`.
- **Behavior:** rasterize (in-memory Pagify for PDF; decode directly for an
  image), run the cascade per page.
- **Output:** `{ "pages": [ {"page":1,"text":"…","engine":"tesseract"} … ],
  "engines": {"tesseract":N,"vision":M} }` — engine tag per page, so a caller
  sees which pages needed the VLM.

This is the "image-data → paged text" tool. The ingest pipeline keeps using the
same cascade internally (it already calls `OCR.Page`), so ingest gets the cheap
tier for free.

### The downstream inversion

- **VLM tier** → corrallm, via agentkit `llm.Client` (`BaseURL`/`VisionModel`
  already in config). corrallm becomes *one OCR backend the tool may choose*, not
  the caller of OCR.
- **Cheap tier** → tesseract or a paddle sidecar — "another OCR system."
- The MCP tool is the boundary; both are implementations.

## 5. Phased build

- ✅ **S1 — cascade core (lib)** (2026-07-21, `2ed812f`). `ocrengine.go`
  (`PageEngine` + `PageOCR`; `TesseractEngine` exec+TSV parse; `PaddleEngine`
  ported; `BuildPageEngine`), `gibberish.go` (ported, stdlib-only), `ocr.go`
  cascade (`PageWithEngine`, cheap trusted unless gibberish, never drops a page,
  reports the engine tag), `config.go` `OCRConfig`, `attachCheapOCR` wiring the
  ingest worker + `ocr` CLI (bad knob → warn + degrade, not fail). Tests: gibberish
  suite, cascade (5 stub cases), `BuildPageEngine`, TSV parse — all green. Note:
  tesseract isn't installed on this box, so a live tesseract run is deferred; its
  absence exercises the tested error-degrades-to-VLM path. Zero-value config keeps
  the old VLM-only behavior.
- ✅ **S2 — MCP `ocr` tool** (2026-07-21, `90534e6`). I/O confirmed with the user:
  in `{path?|data?, mime?}`, out `{pages:[{page,text,engine}], engines:{<e>:n}}`.
  `PagifyBytes` (in-memory rasterization, shared core with Pagify); `ocrtool.go`
  (`loadDoc`, `docIsPDF` via mime/ext/%PDF magic, `ocrDocument` = PDF→pages or
  single image → cascade, `buildToolOCR` offers the tool only when a vision model
  and/or cheap engine exists); registered in serve.go; `visionPage` nil-guards a
  missing VLM. Tests + live MCP stdio smoke (tool advertised, real PNG ran the
  whole pipeline to the designed graceful error). tesseract still absent here, so
  a live successful transcription awaits S3 (install) or a VLM run.
- ✅ **S3 — install ergonomics** (2026-07-22, `7da5186`). No-root tesseract install
  done + recorded (below). `raglit doctor` reports OCR readiness — cheap engine
  (tesseract `--version` / paddleocr reachability), vision endpoint reachability,
  and a verdict (full cascade / VLM-only / cheap-only / unavailable), with the
  exact install hint when tesseract is missing. Validated live across three
  configs (healthy, bad tesseract_bin, unreachable paddleocr).

### Live validation (2026-07-22) — both tiers end-to-end via the MCP `ocr` tool

Closes the live gap S1/S2 left (tesseract wasn't installed). Same clean text
image, driven through `raglit serve` over stdio:

- **cheap** (`cheap_engine: tesseract`): `engine:"tesseract"`, exact text, no VLM.
- **VLM** (`cheap_engine: none`, `vision_model: ternary-bonsai-27b`,
  `base_url: http://127.0.0.1:8111/v1`): routed to corrallm's bonsai vision model,
  `engine:"vision"`, exact text, ~12s.
- **escalation (the whole cascade in one call)**: a wave-warped scan tesseract
  garbled to "The Visiah mel hese pl ge9rd4ed Seal toy" (conf 0.33) — the gate
  tripped, escalated to bonsai, which returned the correct "The vision model
  rescued this degraded scan today", tagged `engine:"vision"`. Cheap→gate→VLM
  rescue, proven end-to-end (~8s).

### No-root tesseract install (Ubuntu 24.04, no sudo) — the "difficult" path

sudo needs a password here, so apt-install isn't available. Extract the debs into
a user prefix instead (all other deps are already system libs):

```
PREFIX=/home/nthalk/local/opt/tesseract
cd $tmp && apt-get download tesseract-ocr tesseract-ocr-eng libtesseract5 liblept5
for d in *.deb; do dpkg-deb -x "$d" "$PREFIX"; done
# wrapper on PATH so `tesseract` just works (bundles libtesseract+liblept, tessdata):
cat > ~/local/bin/tesseract <<EOF
#!/bin/sh
export LD_LIBRARY_PATH="$PREFIX/usr/lib/x86_64-linux-gnu:\$LD_LIBRARY_PATH"
export TESSDATA_PREFIX="$PREFIX/usr/share/tesseract-ocr/5/tessdata"
exec "$PREFIX/usr/bin/tesseract" "\$@"
EOF
chmod +x ~/local/bin/tesseract
```

Result: tesseract 5.3.4, `eng` langdata, on PATH — so raglit's default
`tesseract_bin` resolves with no config. A proper `sudo apt-get install
tesseract-ocr tesseract-ocr-eng` is the clean path if root is available.

- ✅ **S4 — format router** (2026-07-23, `b2bbb05`). "Cascade all the way down":
  each source picks the cheapest extractor. `extract.go` — `ClassifyDoc`
  (pdf/image/office/text), `pdfUnits` = per-page hybrid (pdftotext text layer →
  text unit; else pdftoppm-rasterize → image unit for OCR), `PandocText` for
  office/markup, `ExtractPaged` (the ocr tool's core over any format).
  `store.ingestPDF` now uses the hybrid (born-digital works, no VLM);
  `worker.ingest` routes by kind; `fetch` exposes ContentType; the `ocr` tool is
  rewritten over `ExtractPaged` and offered even with no vision model; `doctor`
  reports poppler + pandoc. Live: born-digital PDF + HTML + real docx → engine
  "text" (no OCR); a hybrid PDF → p1 "text", p2 "tesseract"; poppler was already
  installed, pandoc installed no-root (deb-extract). **Correction to S1**: the
  cheap OCR cascade is used by the `ocr` tool and the PDF-rasterized pages, NOT by
  ingest's SegmentImage (which sends the image straight to the VLM to OCR+segment
  in one call). The earlier "ingest inherits the cascade" note was wrong.

  Deferred within S4: offline born-digital ingest (born-digital PDF text still
  goes through the LLM segmenter; a blank-line offline path would let it index
  with zero LLM). ClassifyDoc unknown → treated as text.

## 6. Open / deferred

- **Cheap-engine provisioning** (S3): the user asked whether raglit should offer
  to install tesseract or a paddle docker. Lean: document both; don't auto-install
  — `raglit doctor` reports presence and the exact install command.
- **ragtag retirement**: broader than this slice; ragtag's other pieces (fetch,
  segment, positions) may or may not already live in raglit — audit separately.
- **Bounding boxes**: ragtag consumed only text + confidence, no coords. Keep
  `Lines` in `PageOCR` for the gate, but the tool's output is text-only unless a
  consumer needs coords.
