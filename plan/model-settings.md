# Model settings in the UI — providers, then models per role

How this plan works: living plan, see ~/CLAUDE.md "Planning". Status marks:
◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

USER 2026-10-22: the UI configures the embedder and vision models; vision may
be unavailable; pattern is PROVIDER then MODEL (corrallm's add-model flow).

## Decisions (USER 2026-10-22)

- Vision None → scans and images are read by tesseract when it is installed.
  Without tesseract they are listed unreadable.
- Embed model change → warn with counts, then queue a re-embed of every index.
  Keyword search keeps working meanwhile.
- Provider presets: Custom (optional bearer token), OpenRouter, LiteLLM.

## Design

- **Config:** `providers: {name: {kind, base_url, api_key}}`. Each role picks
  one: `vision_provider`, `embed_provider`, `segment_provider`,
  `audio_provider`. Model ids stay in the existing fields (`vision_model`, …).
  An empty provider means the legacy `base_url`/`api_key`, so old configs work
  unchanged. `--llm-url/--llm-key` still override every role.
- **Vectors record their model:** `index_meta.embed_model`.
  - On start or reload, an index whose recorded model differs from the
    configured one drops its vectors and backfills them. The backfill embeds
    fragments with no vector, so it survives restarts.
  - Legacy indexes with no record adopt the current model, with no mass
    re-embed on upgrade.
- **API** (admin-only with auth on):
  - `GET /api/settings/models`: providers without keys (`has_key`), roles,
    presets.
  - `PUT /api/settings/models`: validate, write config.json (0600), then the
    daemon reloads itself (SIGHUP path).
  - `POST /api/settings/models/list`: list models for a provider. Takes
    {provider} or {base_url, api_key}, reuses `fetchModels` and its
    capability flags.
  - `GET /api/settings/models/reembed-impact`: index and fragment counts for
    the confirmation.
- **UI:** a Models page.
  1. A provider list with add and edit (preset, name, base URL, optional token).
  2. One card per role: provider select, then model autocomplete, with a
     capability filter when the provider reports capabilities.
  3. None is allowed for vision and audio. Segment empty → the vision model.
  4. Save.

## Slices

1. ◻ Config providers + per-role endpoint resolution (pipeline.go, queuecmd.go STT).
2. ◻ Vision None → tesseract-only OCR (cheap result always accepted; no VLM).
3. ◻ embed_model meta + re-embed backfill.
4. ◻ Settings API + self-reload.
5. ◻ UI page.

**risks:**
- A reload re-execs only when idle, so a busy daemon applies settings late. The
  UI says "applies when the current job finishes".
- The tesseract-only path has no figure descriptions and no LLM segmentation.
- Keys are stored in plain text in config.json (0600), the same as today.

**blocking decisions:** none open.

**optional extensions:**
- A per-index embed model.
- Provider health and latency display.
- Importing models from a corrallm catalog.
