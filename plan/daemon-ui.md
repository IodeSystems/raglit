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

### ✅ 2. The hierarchy — dashboard → project → index → branch

`/` is the daemon: totals, then a card per project. `/p/:project` is its
indexes, with branches nested under the index each overlays. `/i/:index` keeps
every route it had and gains a breadcrumb (`projects / dun / dun`) and a
Branches tab. `GET /api/projects` derives the grouping — see
`cmd/raglit/projectsapi.go` for why there is no projects table.

Indexes stay at `/i/:index` rather than nesting under `/p/:project/i/:index`:
the daemon name is already unique and already what every endpoint takes, so
nesting would duplicate the namespace in the URL and break every existing link.
`/p/` is not in `apiPrefixes`, so the catch-all needed no change.

Branch fork/list/delete had been built since the branch-storage work and shown
nowhere — reachable only by curl, which is why `/api/branches` was empty.

- **fixed on the way**: the fork form sent the branch name RAW, so forking
  `uitest` off `dun__dun` created a top-level index called `uitest` — outside
  the `dun` project, orphaned from the corpus it overlays. It inherits its
  parent's namespace now, and the form shows the name it will get.
- **verified live**: fork → overlay reads the parent's documents → nests under
  the project → delete → gone.

### ✅ 3. Search at project scope

One `SearchPane`, two scopes. `/search?index=` already takes a comma list and a
`prefix*` wildcard and `selectIndexes` expands it, so a project search is the
selector `<project>__*` — the same string `nsSelector` already sends for "search
all" within a namespace — and needed no endpoint. Hits carry their own `index`
and link into it, so a result from `dun__dun-main` opens there and not in the
scope that was searched.

- **still open, inherited**: `daemon-stack.md` records that the `project=` query
  parameter does NOT namespace the index on these endpoints — it answers about
  the plain `default` index instead, confidently and empty. Nothing in the new
  UI uses it (the SPA sends `index=` throughout), so this is now a trap for API
  clients only. Fix or remove it.

### ✅ 4. The fair scheduler

Two lanes, split by RESOURCE rather than by speed (`lane.go`). `heavy` is
vision/OCR: one slot, because the GPU admits one and a second concurrent page
does not start earlier, it blocks inside the server where nothing here can see
it. `light` is everything else — pandoc, mail, spreadsheets, the deterministic
fragmenter, embedding — at three. A 24 MB mail archive is not fast and is still
light; what has to be serial is the GPU slot, not the wall clock.

Each lane runs ONE claimer feeding N runners. The claim decides fairness — it is
what walks the indexes in turn — so several claimers racing one cursor would
hand out whatever they won rather than what is next. The cursor RESUMES between
passes rather than restarting, or the alphabetically-first index with a full
queue takes every turn.

The lane is stored on the row, guessed from the URL at enqueue and corrected by
the worker once it has routed the bytes (recorded as a `lane` stage). Storing it
makes the claim one indexed query per lane and makes the correction survive a
retry.

**Measured live**, which is the whole point:

    14:17:50  heavy:  1 run /  49 wait   light:  0 run / 127 wait
    14:18:16  heavy:  1 run /  49 wait   light:  0 run /  78 wait

One long OCR held the heavy lane for the whole window while light drained ~10
jobs per 5s. Before this, all 78 were behind that one job.

- **three bugs found by building it**, each pinned by a test:
  - **The daemon crash-looped.** `CREATE INDEX ... (state, lane, id)` went into
    `schema.sql`, which runs before `migrate()` on every open — and on an
    existing database `CREATE TABLE IF NOT EXISTS` is a no-op, so the column did
    not exist yet. `schema: SQL logic error: no such column: lane`, at open, so
    systemd restarted it into the same failure. Indexes over migrated columns
    belong in `migrate()`. Every other test builds a FRESH database, where the
    ordering never shows, which is why nothing caught it.
  - **The jobs list broke outright.** Adding a column to `ingest_jobs` without
    adding it to `ListJobs`'s projection fails metaquery's shape check —
    `field IngestJob.Lane not in projection` — so `/api/jobs` returned an error
    for every index.
  - **The queue reported work nobody was doing.** The claim is what writes
    `running`, so a claimer running ahead of its runners marks rows nothing is
    executing: the one-slot heavy lane ran one job and reported two. Claims now
    take a slot token first, so a job stays `pending` — visible as waiting,
    still cancellable — until something can actually run it.
- **risk, untested**: `DefaultLaneSlots` is a package var, not config. Changing
  light's 3 needs a rebuild.

## Active work

Nothing in flight. The four slices above are done and deployed.

## Found on the way

### ✅ Deleting an index or branch raced the worker loop, which put it back

`Registry.Get` CREATES on demand — right for ingest, where POSTing to a new
index name should make it. The worker loops walk `reg.Names()` and then `Get`
each name in turn, so a name deleted between the listing and the open was
recreated as an empty index. Observed while testing the branches tab: the delete
answered `ok`, and `index.sqlite` was back within the same second, listed
everywhere and impossible to remove while the daemon ran.

`Registry.Existing` opens only what is already there; both worker loops use it.
`TestRegistry_ExistingDoesNotResurrectADeletedIndex` pins both halves — that
`Existing` refuses and that `Get` still creates, because ingest depends on it.

**Matters for the scheduler**: a global dispatcher walks names for a living, so
it must claim through `Existing` or reintroduce this.

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

### ◻ `work` carries 6,123 failed jobs against 0 documents

Surfaced by the new overview, which is the first thing that ever totalled them:
`work__work` 1,511 and `work__work-main` 4,612, both with an empty index. With
`dun__dun-main`'s 235 that is essentially the daemon's entire failure count.

- **next**: read the stages on a handful — the job view shows them now — and
  find whether it is one cause repeated thousands of times.
- **assumption to check**: nobody wants that corpus indexed and the failures are
  a watcher pointed at something it cannot read. Not touched.

## Optional extensions (not in scope now)

- **An endpoint-down problem kind.** N documents each reporting the same 503 is
  one fact rendered N times. A probe of the configured endpoint would say it
  once, and say the right thing.
- Dedupe `llm-retries` rows per job — job 927 reports twice, which is correct
  (two runs both retried) and reads as duplication.
