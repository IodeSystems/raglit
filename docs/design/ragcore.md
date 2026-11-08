# ragcore: raglit's engine inside dund

Status: APPROVED (Carl, 2026-11-07); R1-R5 built on branch `ragcore`, tag v0.5.0 waits for Carl. Each R step is an aw4
issue. This plan stays here as design history.

## Decided (Carl)

- dund is its own trust domain. It embeds raglit's engine as a Go library.
- dund keeps code, documentation, memory and strategy indexes, for its own
  work. User document collections stay on raglitd.
- dund organizes its own indexes (per project, global). It does not use
  Registry, namespaces, project keys, ACL, OIDC, the web UI or MCP.
- OCR and PDF/image extraction are in dund's first version.
- raglitd and raglit web are unchanged: daemon per trust domain, keyed API.
- The scheduler and drain move into the library (R3).
- dund passes its own `Models` (R2). It does not read raglit's config.json.
- raglit's session plans and builds R1–R5, with a DAG of subagents.

## What exists (measured on b008935)

- The root package `github.com/iodesystems/raglit` is already the engine:
  Store, branches, queue, Worker, pipeline, extract, OCR, embed, identity,
  search. 33k lines. Its dependency closure (`go list -deps .`) has no OIDC,
  chi or MCP.
- HTTP, auth, MCP, UI and the daemon scheduler are in `cmd/raglit` (23k lines).
- `Worker{Store, OCR, STT, Segmenter, Frag, ...}` is a plain struct.
  `Worker.Run(ctx)` is a one-store loop.
- raglit builds and vets against dun's agentkit v0.25.0 (raglit pins v0.14.1).
  Tested in a scratch worktree: `go get agentkit@v0.25.0`, `go mod tidy`,
  `go build ./...`, `go vet`, `go test ./...`. Every package passes.

## What is missing: the work

The engine is a library, but the parts that make it usable are in `cmd/raglit`.
dund would have to copy them. Each step below moves one into the root package,
with cmd/raglit then calling the moved code. Every step is a refactor with no
behavior change for raglitd; the existing tests are the check.

**R1. Split `attest` into types and service.** Done: 82e5d10, merged 0e37175. `attest/service.go` uses huma
for HTTP errors, and the root package imports `attest` for its types
(attest.Entry, Reading, Unit, Asset). Move the HTTP service to
`attest/service` (or `cmd/raglit`). After this, the root package's closure has
no huma.

**R2. Worker construction.** Done: 48c5e17, merged ff28f1f; fixes 4e637d4 (Channels required), 8bfcca3. `buildWorker` (`cmd/raglit/queuecmd.go:74`,
about 150 lines) does the following:
- builds STT, OCR (vision model, or tesseract alone), the segmenter and the
  retry tally;
- probes the embed and chat input limits;
- sets the identifier and the pool.

It reads `raglit.Config` from a Home and the cmd-only `llmFlags`
(`cmd/raglit/pipeline.go:24`), including the per-model channels (gates). Move
it to the root as `raglit.NewWorker(store, Models)`. `Models` is a plain struct:
endpoints, keys, model ids per role, OCR options. It does not use Home or
flags. raglit fills `Models` from config.json and its flags. dund fills it from
its own config.

**R3. Scheduler and drain.** Done: 8a1cfea; fixes af4776a, 581b423. `runQueue` and `runIngestRunners`
(`cmd/raglit/serve.go:359,381`), the lanes, `jobDrain` (`cmd/raglit/drain.go`),
orphan reclaim and the identity-queue runner all take a `*Registry`. Move them
to the root as `raglit.Runner` over a list of Stores. raglit passes the
Registry's stores. dund passes its own. dund restarts on update, so it needs
the drain (stop claiming, cut off at grace, requeue) as much as raglitd does.
`Worker.Run` alone is not enough: it has no drain, no lanes and no shared
runner limit across indexes.

**R4. Public surface and version tags.** `api_test.go` and the package comment done; the v0.5.0 tag is not done. The surface is:
- `OpenHome` / `OpenIndex`
- `Store`'s ingest, search and branch methods
- `Hit`, `Job` and `JobInfo`
- `NewWorker`, `Models` and `Runner`

Add `api_test.go`, a compile-only test that uses exactly this surface, so a
break fails raglit's own tests before it reaches dun. Tag v0.5.0, then follow
semver from there. dun depends on tags. dun does not use a `replace
../raglit`, which is what breaks aw4 today.

**R5. Field filters before ranking.** Done: 402f210, merged dff199a; fix 80e011e (tags normalized on write). This is needed for memory tags. Add a
predicate next to `pathPredicate` (`store.go:788`). FTS (`searchLocal`) and
the vector scan (`vecSearchLocal`) both put it in their WHERE. RRF fusion is
unchanged. Tags already exist as `documents.gen_content_tags` and
`gen_role_tags` (comma-joined; the schema comment rejects a join table at 3–5
tags per document), so a tag filter matches on those columns. A field filter
uses `json_extract` over `doc_fields.fields`. Filters apply in each branch
layer, the same way the path filter does.

## What the build revealed (for dund's author)

- `Models.Channels` is required. Build one `NewChannels()` per process and pass
  it in every `Models`.
- `NewWorker(store, models, pool)` takes `nil` for the pool; the pool is the
  daemon's cross-index cache.
- `Runner` needs a `StoreSet` (`Names`, `Existing`). `*Registry` is one; a
  five-line type over a fixed set is another (see `api_test.go`).
- The identity queue drains only on a store whose identifier is set. Call
  `Store.SetIdentifier` (from `Models.Identifier()`) before `Runner.Start`.
- A branch needs no Registry: `OpenIndex(home, name)` plus `SetParent(parent)`.
  `BranchDiff` reads the overlay. Registry adds only the on-disk branch.json
  and lifecycle.
- Vector search needs `Store.SetEmbedder`. The test sets none, so it checks
  only the call shape of the vector and hybrid variants, not their results.

## Correction to dun's docs/rag.md

dun's docs/rag.md says dund opens each store with `raglit.Open(path)`. A bare
`Open` has no Home, so originals are not copied, and attachment and
transcript dirs are empty (`store.go:158-176`). Page images for OCR are kept
under the Home. dund should give each index its own directory and call
`raglit.OpenHome(raglit.Home(dir))`. A Home is only a directory layout. It is
not the Registry. dund still decides which indexes exist and where.

## dun's side (for alignment, dun owns it)

- D1, after R1–R4: dund hosts the Runner and the stores, and serves search over
  its socket.
- D2: model-role config and probes. Start from raglit's `Models` (R2), so there
  is one format.
- D3, after R5: memory moves into dund. `/rag on` is no longer needed for
  memory.
- D4: ANN index, only when a measured index outgrows the brute-force scan.

## Build DAG

Every step works in its own sibling worktree (`../raglit.rc-*`), so the sqlc
plugin path `../sqlc-go-codegen-metaquery` resolves and the main checkout is
not rebuilt mid-change. No step changes go.mod.

```
R1 attest split ─┐
R2 NewWorker ────┼─ review each ─ integrate (branch ragcore) ─ R3 Runner ─ review ─ R4 api_test ─ final review
R5 filters ──────┘
```

- Wave 1: R1, R2 and R5 run in parallel. Each step has a reviewer, who checks
  for behavior changes in raglitd and missing tests, and a fix pass.
- Integrate: merge the three branches into `ragcore`, then run the full
  suite.
- R3 waits for R2: it moves the scheduler that calls `NewWorker`.
- R4 fixes the surface after R1–R3 and R5.
- Then local main fast-forwards to `ragcore`. Tagging v0.5.0 needs a push,
  and that waits for Carl.

## Risks

- **agentkit version gap.** In dund, MVS builds raglit with agentkit v0.25.0.
  It builds, and `go test ./...` passes. These are unit tests, not live model
  calls. If raglit raises its own pin to v0.25.0, aw4 needs a matching
  `go mod tidy`.
- **R2 and R3 touch the daemon's hot path** (worker wiring, scheduler, drain).
  The tests cover the scheduler and the drain (`queuesched_test.go`). They do
  not cover buildWorker's probe-and-gate wiring against live models.
- **The branch overlay and field filters.** If a field edit lands in a branch
  without its document row, a parent document still matches the old fields.
  Not checked.
- **A public API freezes code that changes often.** R4's surface is kept small
  for that reason.

## Blocking decisions (Carl)

None open. Tag v0.5.0: done 2026-11-08 (c1cf3a3); its signed manifest waits
for Carl's `make release-manifest`.

## Optional, out of scope

- A Postgres backend and a storage interface: not until a second backend is
  needed.
- Branch merge (raglit P6).
- A move operation for an index (Carl 2026-11-08: dund would like one; not
  required). Without it, an index left behind when a project moves or is
  dropped stays on disk unused. That is accepted.
- Moving `acl.go`, `keys.go` and `Registry` out of the root package. They are
  not in dund's way.
