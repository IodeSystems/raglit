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

- ◻ **S1 — cascade core (lib).** Port `gibberish.go`; add `PageEngine` +
  `TesseractEngine` + `PaddleEngine`; refactor `OCR.Page` into the cascade; config
  plumbing. Tests: gibberish suite (port) + cascade with stub engines (clean→cheap,
  garbled→VLM, empty→empty, cheap-error→VLM). No MCP change; ingest inherits it.
  **next**: confirm the `PageEngine`/`PageOCR` names before porting.
  **risks**: tesseract confidence/tsv parsing is fiddly — coarse confidence is
  fine for the gate; the lexical test carries the "confident garbage" case.
- ◻ **S2 — MCP `ocr` tool.** In-memory Pagify; register the tool in serve.go;
  paged-text JSON. Checkpoint the exact tool I/O schema with the user first.
- ◻ **S3 (opt) — install ergonomics.** `raglit doctor` / README for installing
  tesseract or a paddle docker; clear message when `cheap_engine` is set but the
  binary/URL is unreachable (degrade to VLM, don't fail).

## 6. Open / deferred

- **Cheap-engine provisioning** (S3): the user asked whether raglit should offer
  to install tesseract or a paddle docker. Lean: document both; don't auto-install
  — `raglit doctor` reports presence and the exact install command.
- **ragtag retirement**: broader than this slice; ragtag's other pieces (fetch,
  segment, positions) may or may not already live in raglit — audit separately.
- **Bounding boxes**: ragtag consumed only text + confidence, no coords. Keep
  `Lines` in `PageOCR` for the gate, but the tool's output is text-only unless a
  consumer needs coords.
