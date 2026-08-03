# raglit: one SPA — path routing, merged attest, document notes

Status: ◐ in progress, started 2026-08-03. Living doc — prune as slices land.
Supersedes the routing half of `review-ui.md` (which stays as the record of what
the vanilla page delivered).

How this plan works: see `~/CLAUDE.md` § Planning. Status marks: ◻ todo ·
◐ in progress · ✅ done · ⏸ parked · ❓ blocked. Completed trees move to
`plan/done.md`; deferred next-steps move to `plan/icebox.md`.

## Goal (from user)

A single-page app with **browser location routing** (not hash), where the
selected index and the document being viewed are both in the URL, sub-tabs and
pages too. TanStack Router with hierarchical rendering, so a search bar can live
in the shell above every route. People who know a corpus can **add notes and
comments** to a document and **re-title** it — the case that prompted it: a
document was auto-titled as if it were the survey, when it is somebody's
annotation OF the survey, and the title said nothing about that.

## What was actually there (corrected premise)

There was no React. Two hand-written vanilla-JS pages, each a single
`//go:embed`'d HTML file:

- `cmd/raglit/ui.html` (930 lines) — the daemon's surface. **Now deleted**,
  replaced by `web/`.
- `attest/ui.html` (1060 lines) — the review workbench. **Untouched**; still
  mounted per index at `/attest/<name>` and served by `raglit attest`.

Routing already existed, in the hash, with a written reason
(`cmd/raglit/ui.html:251`): path routing needs the daemon to catch-all every
unknown URL, "which would swallow the attest mount and every API route with one
typo." Answered rather than ignored — the catch-all is a deny-list of the
mounted prefixes (`cmd/raglit/webui.go`), and two tests pin it.

## Decisions

**Vite + React + TanStack Router, `dist` embedded.** Cost is real and named: a
build step, `node_modules`, generated assets in the tree.

**`web` is a Go package.** Not a preference — `//go:embed` cannot reach outside
the declaring package's directory, so `../../web/dist` is not a legal pattern.
`web/embed.go` puts the embed beside the thing it embeds.

**`web/dist` is COMMITTED.** `go install …/cmd/raglit@latest` must work with no
node, and `//go:embed` only sees files present in the module. Generated: never
hand-edit, rebuild with `make web`.

**Code-based routes, not file-based codegen.** ~15 routes; code-splitting buys
nothing in a binary-embedded bundle, and the generated `routeTree.gen.ts` would
be a file nobody may edit for no gain.

**Notes are per-document AND per-page** (nullable `page`).

**Re-title needed no new storage.** `POST /api/identify` already records a
person's name/summary/kind, and `identity.go:443` already guarantees a machine
re-run will not overwrite it.

### URL scheme

The document path is ONE encoded segment — and the router already does that.
TanStack's `encodePathParam` is `encodeURIComponent`, so a slash becomes `%2F`
with no help. **Do not add a `params.stringify`**: one was added, it encoded on
top of TanStack's own encoding, and every document URL became `%252F…` —
functional, because the extra decode cancelled it, and unreadable. See the
comment at the top of `web/src/router.tsx`.

    /                                     → redirect to the first index
    /i/:index                             → dashboard
    /i/:index/health
    /i/:index/jobs[/:jobId]
    /i/:index/search?q=&mode=
    /i/:index/d                           → document list
    /i/:index/d/:doc                      → redirects to /pages
    /i/:index/d/:doc/pages[/:page]
    /i/:index/d/:doc/transcript|seen|history|notes
    /i/:index/attest                      → the workbench, index-level
    /i/:index/attest/a/:asset

Old `#/documents/<index>/<path>/<sub>` links are rewritten on load, in
`web/src/legacyHash.ts` — which runs BEFORE the router is created. As an effect
it lost a race it could not win: `/` redirects in `beforeLoad`, so the hash was
gone before any component mounted and the legacy link silently became the
dashboard.

## Done

### ✅ 1. Scaffolding — `web/`, embed, build

`web/` with vite + React 19 + TanStack Router 1.170; `web/embed.go`;
`make web` / `make web-dev`. `web` is deliberately NOT part of `make build` —
a Go build must keep working with no node, which is why dist is in the tree.

- **untested**: a clean-clone `go install` on a machine with no node. The
  arrangement is designed for it and `go build ./...` works here, but the
  no-node case has not actually been run.

### ✅ 2. Path routing + the chi catch-all

`router.NotFound(spa)` plus the deny-list in `cmd/raglit/webui.go`. Pinned by
`TestSPA_RoutesServeTheDocument`, `TestSPA_DoesNotSwallowTheAPI`,
`TestSPA_APIStillAnswers`, `TestSPA_ServesItsAssets`.

- **the standing risk**: widening `apiPrefixes` wrongly makes a real route 404;
  narrowing it wrongly makes a mistyped API path answer with HTML, which
  presents to a client as a JSON parse error and sends somebody debugging the
  encoder. Keep it in step with `API_PREFIXES` in `web/vite.config.ts`.

### ✅ 3. The daemon panes, ported

dashboard · health (grouped problems, per-kind actions, shared-grounds
collapsing) · jobs (retry/cancel/forget, `:jobId` focus) · document list ·
pages (eager images, lightbox, re-OCR, figures) · transcript · seen-in ·
history · document actions (download / re-ingest / re-read, in-flight gated).

Behaviour deliberately carried across with its reasons, not just its output:
page images are NOT `loading="lazy"` (zero-height deadlock), search snippets
render as TEXT (corpus content can contain markup), `fresh:true` on re-ingest,
the `download` filename, and the closed identity-kind vocabulary.

### ✅ 5. Notes + re-title

`document_notes` (sql/schema.sql), `notes.go`, `cmd/raglit/notesapi.go`
(`GET/POST /api/notes`, `POST /api/notes/delete`), the `notes` sub-tab and the
re-title form. `TestNotesAPI` covers round-trip, empty-body refusal,
unknown-path refusal, delete.

### ✅ 6. The shell search bar

In `IndexShell`, above every route — which is what the nested tree was for. It
keeps its text and focus across navigation; a flat tree would remount it.

## Active work

### ◐ 4. The attest workbench — merged, but not wholly

**Landed**: `/i/:index/attest` lists assets with resolved completeness, and
`/i/:index/attest/a/:asset` shows units and records verdicts against the
already-mounted `/api/attest/:index`. Verified live: a sweep, then a `confirmed`
verdict, then 1/1 ruled.

**Not ported**: audio. `attest/ui.html` has a player built for scrubbing a
two-hour hearing — gap-skipping, per-turn seek, Range-served media. An audio
asset links out to that page rather than being shown badly here.

- **next**: port the player, or decide it stays where it is.
- **blocking decision (USER)**: `attest.Service.UI(apiBase, assetBase)` is a
  PUBLISHED contract — attest is built to be mounted by arbitrary hosts and its
  self-contained page is part of what they mount. **Assumed and implemented**:
  `attest/ui.html` is untouched and remains the package's own fallback; the SPA
  supersedes it in the daemon only. `raglit attest` still serves the old page —
  it was NOT switched to the SPA, because the SPA assumes the daemon's API
  surface (`/indexes`, `/status`, `/api/documents`) that a bare attest mount
  does not have. Surface this before changing it.

## Found on the way (not the SPA's doing)

### ◻ `delano-v-mckinnon__default` has 43 stale document rows

Surfaced by the new `missing-file` problem kind, which exists because of this.
**Nothing is lost** — all 43 files were located on disk. A row is a pointer; a
moved file breaks the pointer, not the document.

- Most: a deliberate archival move, `documents/` → `legacy/` (git says `R100`).
  0 rows under `legacy/` — the new locations were never ingested.
- Four court filings were RENAMED and never re-ingested, so they are on disk
  under better names and **absent from the corpus**: the Kelly Wynn and Kristine
  Tamman declarations, the partial-SJ motion, the Form-35R inspection response.
  The access-permit rename WAS re-ingested, so this was inconsistent, not
  systematic.
- **next**: ingest the new paths, then forget the dead rows — in that order.
- **assumption to check**: nobody has re-ingested `legacy/` on purpose to keep
  archived material out of the corpus. If that was the intent, the fix is a
  withdrawal (which records grounds), not a forget.

### ◻ `raglit sync` cannot run on that project

`config.json` has no `indexes` roots. Not changed — editing a real project's
config is the user's call.

### ◻ HEAD on attest's source route is 405

`attest.Service` registers `/source` with `router.Method(http.MethodGet, …)`, so
`http.ServeFile`'s built-in HEAD handling never runs. GET is a correct 200 with
`Accept-Ranges`, so downloads work and this is invisible to the UI — but any
HEAD-based link checker or monitor calls the evidence unreachable. In the
published `attest` package, so not touched unilaterally.

## Optional extensions (not in scope now)

- Editing a note; threading/replies; notes surfaced in search.
- Live push instead of 3s polling.
- Auth. The daemon has none and this does not change that.
- A note survives re-ingest (ids are stable under `ON CONFLICT(path) DO UPDATE`)
  but not a forget. That is believed right; nobody has been asked.
