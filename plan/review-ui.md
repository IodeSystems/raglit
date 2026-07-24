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
