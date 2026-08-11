# raglit: review UI — status, job control, OCR review

Status: done 2026-07-23. Living doc — prune as follow-ups land.

## Goal (from user)

An HTTP status/control/review UI: look at index status/size, jobs, ETA; control
jobs; and review OCR (per-page text + engine tag + page image, flagging pages
that escalated to the VLM).

## Delivered

Served by the existing `raglit daemon` at `/` (control plane under `/api/*`);
`raglit review` is the same server with a friendlier banner. Self-contained HTML
(`cmd/raglit/ui.html`, `//go:embed` via `ui.go`) — no external assets, theme-aware.

- **Status** — cards (docs, fragments, done/running/pending/failed, jobs/min),
  auto-refresh every 3s. Reuses `/status` + `/indexes`.
- **Job control** — `GET /api/jobs?index=&state=&limit=` lists all jobs (ETA
  folded in from the status snapshot). `POST /api/jobs/retry` (error|done →
  pending, cleared) and `POST /api/jobs/cancel` (pending → deleted). Store:
  `Jobs`, `RetryJob`, `CancelJob` in `queue.go`.
- **OCR review** — `GET /api/documents` (per-doc fragment/page/engine counts),
  `GET /api/doc?path=` (per-page: engine, vision flag, has_image, indexed text),
  `GET /api/page-image?path=&page=` (serves the saved PNG, bounded to the home's
  pages/ dir), `POST /api/reocr` (reruns the cheap→gate→VLM cascade on a saved
  page image → {engine,text}). Store: `review.go`.

### Data model

New `ocr_pages(doc_id, page, engine, image_path)` table (`store.go` schema;
`CREATE TABLE IF NOT EXISTS`, so existing DBs migrate on open). Ingest records
provenance per page in `ingestUnits` (`pipeline.go`): page ≥ 1 only (text
windows are page 0); text unit → engine "text", image unit → engine "vision"
(the VLM OCR'd it during `SegmentImage`) with the page image saved to
`<home>/pages/<tag>/pNNN.png` via `savePageImage`. `beginDoc` clears `ocr_pages`
on reingest. Page "text" in review = the fragments indexed for that page.

### Key architectural note

Ingest OCRs+segments an image page in ONE VLM call (`SegmentImage`) — it does
NOT run the cheap→gate→VLM cascade, so ingest can only tag a page "text"
(born-digital) vs "vision" (needed the VLM). The cascade (and its cheap-tier
escalation) is surfaced on demand via `/api/reocr` against the saved page image.

## Verified

- Unit (`review_test.go`): job retry/cancel/list + state guards; DocReview page
  provenance + text-from-fragments; Documents engine breakdown; reingest clears
  pages. Full suite green.
- Live: offline text ingest → daemon → status/jobs/documents correct; retry an
  errored job → pending, cancel it → gone; cancel-done → 400; page-image for a
  text doc → 404. Image ingest via corrallm (Qwen3-6-27B-MPT vision) → doc shows
  vision:1; `/api/doc` returns page text + engine "vision" + has_image; page-image
  serves the PNG; `/api/reocr` reran the configured cascade (VLM-only here, so
  engine "vision" — cheap tier off in that config).

## Deferred / not done

- No auth (localhost only) — matches the daemon's existing stance.
- reocr shows the cheap→VLM escalation only when a cheap engine is configured
  (`ocr.cheap_engine`); with the default `none` it's VLM-only.
- No bulk "retry all failed" / job delete-done; no live push (polling only).
- Docs indexed before this feature have no `ocr_pages` rows → review shows "no
  OCR-tracked pages" until re-ingested.

## Text | Layout on the page card

The page list (`/i/<index>/d/<doc>/pages`) and the single-page screen show, per
page: the image with the reader's layout blocks drawn on it, and two tabs —
**Text** (what the index holds, what search matched) and **Layout** (the blocks,
and the raw transcription behind them). The tabs appear ONLY where the page has
layout blocks; `has_layout` rides along with the page list so a tab strip never
appears over a tesseract page and then turns out empty.

Built first as a separate server-rendered `/layout` route, which was wrong: the
per-page screen already existed and already showed "the image, what was read
from it, what the machine saw". A second surface for the same thing is a second
thing to keep in step. Removed; `GET /api/page-layout` (the data) stayed.

Why it exists: the flattener (indextext.go) deliberately drops `data-bbox` and
`data-label` before indexing, and until this there was NO surface for them —
`bbox` appeared in the codebase only as a figure's location in a search hit. A
page could differ from its indexed text by 40% of its bytes with nothing to show
it. The byte counts are printed beside the tabs for exactly that reason.

Three things that made it cheap:
  - The raw markup survives in `ocr_page_cache`, keyed by the page image's
    sha256 — independent of the flattener, of `writeback_transcription_md` and
    of re-ingests. A changed image correctly misses, because the old boxes
    describe a picture that no longer exists.
  - Coordinates are normalised 0-1000 PER AXIS, INDEPENDENTLY — not pixels, not
    aspect-preserving. So boxes place as CSS percentages: no image dimensions,
    correct at any width, and correct before the image has even loaded. Verified
    by overlay on a 2550x3300 scan whose boxes topped out at 924 x 934.
  - `PagesWithLayout` answers the whole document at once, memoised on
    path+size+mtime. Asking per card would hash a 30-page bundle of 5 MB scans
    on every view — 150 MB of hashing to draw a tab strip.

Parser note: attributes are pulled from the whole matched `<div>` rather than
ordered in one pattern. With `data-bbox` first and `data-label` in an optional
group, the lazy run between them matches empty and EVERY box comes back
unlabelled — caught by a test, not by looking at it.

### The Layout tab rebuilds the page, it does not list fragments

First version listed the blocks. That is a list of micro-fragments: it says what
was read and not how the page was laid out, which is the only question the layout
data can answer. Now each block's text is placed at its own box and sized to fit,
so a form reads as a form and the initial blocks sit where the initials are.

Needs the page's SHAPE, which normalised boxes do not carry — `img_w`/`img_h`
come from `image.DecodeConfig`, a header-only read (a few KB, not a 5 MB decode),
memoised beside the sha.

SIZING, and the version that was wrong: seeding from the box HEIGHT and shrinking
12% per step works for a one-line box and destroys a paragraph — a legal
description in a short wide box overflowed immediately and shrank through twenty
steps to 4% of its start. The page rendered as grey dust. Now: seed from the box
AREA against the character count (how big can a glyph be if N of them must tile
this rectangle), clamp to the box height, then BINARY SEARCH the largest size
that does not overflow. Seven bounded steps, and it FILLS the box rather than
merely fitting inside it. Re-fits on resize, because the container is
percentage-width and every fitted pixel size goes stale with the window.

Known and left: chandra's boxes sometimes overlap, so two blocks can print over
each other. The boxes are the model's, not ours; drawing them faithfully is the
point, and silently nudging them apart would make the view a nicer lie.
