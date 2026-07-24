# raglit: per-job pipeline stages, mode marker, and the OCR/segment split

Status: done 2026-07-23.

## Ask (from user)

Add an offline/llm mode marker on jobs. Ideally every ingest item carries a
series of tasks: a scanned printout runs img→paged-text (OCR) then paged-text→
fragments; a large file like code goes file→fragments directly. Decision: SPLIT
OCR from segmentation now (chosen over record-and-split-later).

## Delivered

### Pipeline split (the behavior change)
`ingestUnits` (pipeline.go) no longer fuses OCR+segment for image units. An IMAGE
unit is now OCR'd to text FIRST by the cascade (`OCR.PageWithEngine`:
cheap→gate→VLM) — the "ocr" task — then segmented as text (`SegmentText`). A TEXT
unit (born-digital PDF page / text window) skips OCR and segments directly. Net
effects: clean scanned pages can be OCR'd cheaply/offline (tesseract) instead of
always paying the VLM; per-page provenance (`ocr_pages.engine`) now records the
REAL cascade engine (tesseract/paddleocr/vision) instead of a fixed "vision", so
the review UI shows genuine escalation. `ingestUnits` gained `ocr *OCR` + a
`*StageLog`; `ingestPDF`/`ingestText` thread them (nil ocr for text).

### Mode marker
`ingest_jobs.mode` ∈ {llm, offline, ""}. Set by the worker: LLM segmenter → "llm";
the dependency-free blank-line split (no model) → "offline". `completeJob` takes
mode; `JobInfo.Mode` + `/api/jobs` expose it; UI shows a mode badge.

### Per-job stages (the "series of tasks")
New `job_stages(job_id, seq, name, engine, state, detail, at)` table + a
`StageLog` recorder (stages.go, nil = no-op so CLI ingest records nothing). The
worker records fetch → extract → [ocr] → segment → [embed] → commit as it runs,
each tagged with its engine (text-layer/pandoc/tesseract/vision/llm/offline/…);
on failure the erroring stage is marked `error` with the message, so you see
exactly which task broke. `/api/jobs` returns `stages[]` per job; the UI expands a
job row into stage chips (green/red dot per state).

### Migration
`mode` is added to existing DBs via `migrate()` in `Open` (pragma-checked ALTER;
`job_stages` is CREATE IF NOT EXISTS). Verified on the pre-existing dogfood DB.

## Verified

- Unit (`stages_test.go`): offline job → mode "offline" + stages
  fetch/extract/segment/commit; LLM job → mode "llm", segment engine "llm";
  OCR-split → provenance engine "vision", stages include ocr before segment.
  Atomic + continuation + no-embedder tests still green (`-race` clean).
- Live: re-ingested README.md offline into the dogfood index → job #50 mode
  "offline", stages fetch→extract(text)→segment(offline)→commit, visible via
  `/api/jobs` and the UI (http://192.168.1.76:7420/).

## Deferred / not done

- Fully-offline IMAGE ingest (tesseract OCR + offline split, no LLM) is possible
  now that OCR and segment are separate, but the worker still builds OCR only when
  a vision model is set and always segments image-derived text with the LLM
  (mode "llm"). Wiring a cheap-only OCR + offline segment for images is a
  follow-up.
- Cost note: a scanned page that needs VLM OCR now costs VLM-OCR + LLM-segment
  (two calls) vs the old single fused call; clean pages get cheaper (tesseract +
  segment, or fully offline). Accepted trade for the cascade + real provenance.
- Stages are per-job SUMMARY tasks (one "ocr", one "segment"); per-page OCR detail
  lives in the OCR-review panel, not as N stage rows.
