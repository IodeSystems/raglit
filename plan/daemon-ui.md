# raglit daemon UI: a hierarchy, and a queue that is fair

Status: ◐ in progress, started 2026-08-11. Living doc — prune as slices land.
Continues `spa-ui.md`, which delivered the SPA this reorganises.

How this plan works: see `~/CLAUDE.md` § Planning. Status marks: ◻ todo ·
◐ in progress · ✅ done · ⏸ parked · ❓ blocked. Completed trees move to
`plan/done.md`; deferred next-steps move to `plan/icebox.md`.

## Goal (from user)

The daemon UI's organisation is bad. Wanted: a **global dashboard**; clicking a
**project** shows its **indexes**; clicking an index shows its **branches**.
Search must work at project OR index scope. Separately, the ingest queue is
**shared across all projects** and its scheduler must be **fair across job
kinds** — embedding is fast and must not queue behind a slow OCR job. And the
Health tab is full of items that do not resolve: a row names a failure and
offers no way to see it or fix it.

## What is actually there (measured on the live daemon, :7420)

- **The queue is not shared.** Each index owns an `ingest_jobs` table inside its
  own `index.sqlite`. `runIndexWorkers` (cmd/raglit/serve.go:277) is ONE serial
  loop that round-robins a single job per index, so a forty-minute OCR job holds
  every other index's turn. There are no job classes at all: embedding is a
  STAGE inside a job, not a queue of its own.
- **A project is not a daemon concept.** It is a client-side `<project>__<index>`
  prefix on the index name (`cmd/raglit/namespace.go`). The daemon sees 9 flat
  names. `/api/watch` knows a project home for the ones registered — 1 of 4.
- **Branches exist and are invisible.** `Registry.ListBranches` and
  `/api/branches` are built; the list is empty and the SPA never shows them.

## Decisions (user, 2026-08-11)

**Queue: per-index tables, global dispatcher.** Rows stay where they are; a
daemon-level scheduler claims across every index. No migration, embedded/CLI
mode is untouched, and deleting an index cannot orphan queue rows.

**Lanes: heavy (vision/OCR) + light (text/office/email), run concurrently.**
Classify at enqueue from the extension, correct after routing. Heavy
concurrency 1 — the 5090 is single-admission anyway, which is the whole reason
OCR moved to chandra — light 2-4 in parallel. Round-robin across projects and
indexes WITHIN a lane, so fairness is two-dimensional.

**Projects: derived from the name prefix, enriched from the watch registry.**
Zero storage, works for all 9 existing indexes on day one.

## Done

### ✅ 1. The Health tab resolves

Four separate defects, each independently enough to make a row dead:

- **A cited job could not be reached.** The health row links AT a job id and the
  jobs view fetched the newest 200 and looked for it there. The cited job was
  927; the newest was 1467. `/api/jobs?id=` + `Store.Job(id)` answer for one job
  whatever its age; `TestGatDaemon_JobByID` reproduces the burial and pins it.
- **The jobs table read fields the API has never returned.** `target` and
  `detail` — the daemon sends `url`, and stage details live on `stages[]`. Both
  columns had been blank on every row for as long as the table existed.
- **Stages were returned and never rendered.** `/api/jobs` has always carried
  the full pipeline account. `/i/:index/jobs/:id` is now a real detail view.
- **`page-unread` rendered NOWHERE.** `Health.tsx` filtered problems through a
  hardcoded `KINDS` list, so a kind it did not name was dropped — and it did not
  name the one that reports the salvage holes. Two sat in the delano index and
  the endpoint reported them on every poll. The list is now a source of WORDING;
  anything unknown still gets a group. A view whose job is to surface what is
  wrong must not be able to discard a problem because a constant went stale.

Also: `llm-retries` got actions (Re-ingest / Dismiss) — it reports a job that
SUCCEEDED, so it had none — and the subject path now links to the document while
the `#id` badge links to the job, each going where its name says.

**Stage ordering was hiding the answer.** A retried job appends a whole second
run of stages under the same `job_id` with `seq` restarting at 1. Ordered by
`seq` the runs interleave: job 927 read as "fetch, fetch, fetch, fetch, extract,
extract, …" — every fact present, nothing attributable. `Store.JobStages` now
sorts by recorded time and the view groups into "Attempt N of 4". Job 927 reads
as four attempts: embed died on input-too-large, segment on a 503, segment on a
stream cancel, then it completed. `ListJobStages` selects `id` and orders by it
— see the codegen note below for why that took two passes.

- Verified live on the delano index after `go install` + `systemctl --user
  restart raglit`: the badge resolves, all four attempts render, `page-unread`
  shows with its Re-read.

## Active work

### ◻ 2. The hierarchy — dashboard → project → index → branch

- **next**: a global dashboard route; `/p/:project` listing its indexes;
  branches under an index. Derive projects by splitting index names on `__`,
  enriched from `/api/watch`.
- **risk**: `spa-ui.md` records that widening the SPA catch-all's deny-list
  wrongly 404s a real route and narrowing it makes a mistyped API path answer
  HTML. New top-level route prefixes must stay in step with `apiPrefixes`
  (cmd/raglit/webui.go) and `API_PREFIXES` (web/vite.config.ts).
- **assumption**: an index name with no `__` belongs to no project. `default` is
  the only one today.

### ◻ 3. Search at project scope

`/search?index=` already takes a comma list and a `prefix*` wildcard, and
`selectIndexes` expands it — so project search is `?index=<proj>__*` and needs
no new endpoint. **But** `daemon-stack.md` records a live bug: the `project=`
query parameter does NOT namespace the index on these endpoints, and answers
about the plain `default` index instead — confidently, and empty. Fix or remove
that parameter as part of this.

### ◻ 4. The fair scheduler

- **next**: lane classification at enqueue; a dispatcher that claims per lane
  with per-lane concurrency; round-robin across (project, index) within a lane.
- **risk**: `claimNextJob` is per-Store and takes the oldest pending row. Two
  lanes claiming concurrently against one index must not both take the same row
  — the claim is already transactional, but nothing has ever run two of them.
- **blocking decision (USER)**: none outstanding; the four choices above are made.

## Found on the way

### ✅ Codegen must run OUR sqlc — `make generate`, never `sqlc generate`

I typed `sqlc generate`, got `/home/nthalk/go/bin/sqlc` (upstream v1.30.0), and
it relocated two queries' trailing `= ?;` to the FRONT, exiting zero. ~40 tests
then failed with `SQL logic error: near "=": syntax error` — a message about the
generated Go that says nothing about the cause. I recorded that as "the
toolchain is broken" and worked around it. **Wrong diagnosis** (user corrected
it): the bug is multibyte, it is already fixed, and the fix was not wired up.

`../sqlc`, branch `fix/sqlite-rune-offsets`: antlr's `NewInputStream` stores its
input as `[]rune`, so every position the sqlite engine reads out of the parse
tree is a RUNE index, while `source.Mutate` slices the same string in BYTES. One
multibyte character earlier in a statement shifts the two apart — and a
statement carries its leading comments, because that is how `-- name:` is found.
So an em-dash in a comment is enough. Mine were.

The trap: an all-ASCII file generates correctly, so nothing springs it until
somebody writes a comment with punctuation in it.

Wired up: `make generate` builds the fork into `./bin/sqlc` (gitignored) and
runs it, and fails loudly if the sibling checkout is missing. It never falls
back to PATH. `ListJobStages` now selects `id` and orders by it, which is what
the stage grouping wanted; the `sort.SliceStable` workaround is gone.

- **standing risk**: `sqlc generate` typed by hand still finds the PATH binary
  and still corrupts silently. The Makefile comment says so; nothing enforces it.
- **note**: regenerating also picks up genuine drift — `internal/db/models.go`
  gains structs for tables added to `schema.sql` since the last regen
  (attestations, document_notes, identity_jobs, and new documents/fragments
  columns). Additive, suite green.

### ⏸ llm.iodesystems.com was returning 503 for everything, 2026-08-11

`/v1/models` and `/v1/embeddings` both answered a proxy's HTML 503 page. Every
ingest failed at the embed stage; the `503 <!DOCTYPE html>` in the health report
is that page. Not a raglit fault, and worth a health signal of its own: the UI
reports it once per document when the correct statement is "the endpoint is
down". See the optional extension below.

## Optional extensions (not in scope now)

- **An endpoint-down problem kind.** N documents each reporting the same 503 is
  one fact rendered N times. A probe of the configured endpoint would say it
  once, and say the right thing.
- Dedupe `llm-retries` rows per job — job 927 reports twice, which is correct
  (two runs both retried) and reads as duplication.
