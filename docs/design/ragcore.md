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

None open. Tag v0.5.0: done 2026-11-08 (c1cf3a3), with its signed manifest.


## R6: what dund's first build asked for (APPROVED 2026-11-08)

Status (v0.6.0 build, branch v060): R6a-f and R7a, R7b, R7d DONE; R7c stays
deferred. Commits: R6c Done covers captions, R7a Documents/ContentHash, R7d
page_readings sweep 5cb87f3; R6d atomic SetEmbedder, R6e embed_dim and
DropVectors cb92e29 (fix: a reembed leaves no old vectors 1b2ab80); R6b, R7b
SetTags c9d4b34; follow-up fixes c685a51 (cut-off captions requeue, ContentHash
pin) and e8fc84b (pending_tags in the removal sweep); R6a, R6f, R7a pins in
api_test.go and the package comment in the pins commit on top. Build notes:
`DocSummary.Hash` was never added, `ContentHash` is the pinned name (see R7a).
`Prune` is pinned as `Prune(dry, deleteOriginals bool) (PruneResult, error)`;
dund collects originals with `Prune(false, true)`, and `Prune(true, false)` is
the dry report. The pin test ingests directly, tags `kind:strategy`, filters by
that tag (and not by `kind`), lists, and removes.

dun-bb built dund against v0.5.1 (dun branch ragcore-dund, 2a7eb32). It works
as is. It uses three exports that api_test.go does not pin, and it found two
gaps. Nothing here blocks dund.

**R6a. Pin what dund already uses.** Add these to api_test.go:
- `NewEmbedder(VectorClient, model)`. This also pins the `VectorClient`
  interface, which the agentkit llm client satisfies.
- `Models.Identifier()`.
- `Document{Path, Title, Fragments}` and `Fragment{Page, Ord, Text}`, so dund
  can call `Store.Ingest` directly for local text instead of queueing a job.
These are additions only, with no code change.

**R6b. Caller-owned tags with no caption fragment.** Today the only way a
caller can set tags is `SupplyIdentity`. It writes a caption, and the caption
is a searchable fragment, so a tagged document can come back twice in the
hits. Tags also cannot go in `gen_content_tags`, because the identity model
rewrites that column on every caption (identity.go:1082).

Plan:
- A new `documents.tags` column that only the caller writes. Identity never
  touches it.
- `Store.SetTags(path, tags []string) error` replaces the document's tags,
  normalized the same way as the other tags. Called on a path that has not
  been ingested yet, it holds the tags pending and the next commit of that
  path applies them, the same way a supplied identity is consumed. So
  "enqueue, then tag" works without waiting for the job.
- `SearchFilter.Tags` matches `tags`, `gen_content_tags` and `gen_role_tags`.
- No fragment is written. Tags are filter data, not text.
- Migration: add the column with default ''. Existing indexes are unaffected.

**R6c. `Runner.Done()` covers the caption goroutines.** Done closes when
ingest returns. The identity loop (`runIdentity`, runner.go:322) runs under
the claim context and has no done signal. An embedder that closes its stores
after `CutOff` can therefore close one under a caption in progress. Change:
Done closes only after both loops have returned. raglitd waits on the same
signal, so a stop can now wait for one in-flight caption call. That call has
already been cancelled through the claim context, so the wait is short.

**R6d. `SetEmbedder` atomic.** Today it is a plain field write
(store.go:147), while `SetIdentifier` is an `atomic.Pointer`. dund's reembed
swaps the embedder while an ingest job may be running on that index, which is
a data race. Fix: make it an `atomic.Pointer[Embedder]` the same way, and
cover it with a `-race` test. Until this lands, dund refuses a reembed while
that index has active jobs.

**R6e. A changed vector size drops the vectors.** `SyncEmbedModel(model)`
(reembed.go:28) drops the vectors only when the model id changes. If the same
id now returns a different dimension, the old vectors stay and cannot be used.
dund records an `embed_dim` key in index_meta itself. Plan: raglit owns that
key. `SyncEmbedModel` takes the dimension, records it next to `embed_model`
and drops the vectors when either one differs. Also add
`Store.DropVectors()`, which drops the vectors unconditionally, for a forced
reembed. dund then stops writing `embed_dim` itself.

**R6f. Pin the rest of what dund uses:**
- the vector calls: `EmbedModel`, `SyncEmbedModel` (with its new signature),
  `MissingVectors`, `BackfillVectors`, `DropVectors`;
- the index metadata calls: `Meta`, `SetMeta`;
- segmentation: `NewSegmenter(Chatter)`, `Segmenter.SegmentText`,
  `SegResult.Degraded`.

`SupplyIdentity` is not pinned. R6b's `SetTags` replaces dund's use of it,
which is tags only. A caption that should be searchable still goes through
`SupplyIdentity`, and that stays unpinned until someone needs it.

**Version:** v0.6.0. These are additions, plus two changes: the
`SyncEmbedModel` signature, and what Done waits for. Before 1.0 a minor
version can break; dund is the only embedder.

**Blocking decisions (Carl), for R6–R8:**
1. R6b tags: DECIDED. A separate caller-owned `tags` column (Carl,
   2026-11-08).
2. Release: DECIDED. R6 and R7 ship as v0.6.0, R8 as v0.7.0 (Carl,
   2026-11-08).
3. R8 code segmenter: DECIDED (Carl, 2026-11-08). poly-lsp-mcp exports a
   lightweight segmenter. raglit's core accepts one through an interface and
   does not import poly-lsp-mcp. raglitd (cmd/raglit) and dund both import
   poly-lsp-mcp and plug it in. Plan below, under "R8: code segmenter".

## R7: dund's full list (APPROVED 2026-11-08)

dun-bb sent everything dund needs for the remaining slices: memory,
strategies, then code and docs. Each item below was checked against the code;
the file:line references come from that check. R7 covers what dund uses now.
R8 covers what the code index will need.

**R7a. Listing and pins for what dund uses today.**
- `Store.Documents()` (branch.go:173) returns `DocSummary` (review.go:55)
  without the hash. Add `Hash` (`documents.content_hash`). Pin `Documents`
  and `DocSummary`, so dund can reconcile a directory in one call. Pin
  `DocumentHash` with its format: sha256 hex of the raw source bytes
  (worker.go:250; schema.sql:10), not of the extracted text.
- `DocumentAddedAt` is not pinned. The listing carries `AddedAt`, which is the
  time of the last real ingest (worker.go:325 skips an unchanged file before
  commit). Document that.
- Correction at build (2026-11-08): `DocSummary` already carried
  `ContentHash` (`documents.content_hash`, json `content_hash`). A second
  field with the same value was not added; `ContentHash` is the pinned name.
- Pin `RemoveDocument(path, stored)` (docfiles.go:27). `stored` also deletes
  the document's bytes, but only if they are in this index's own storage (an
  upload or an extracted attachment).
- Pin `NewChannels()`, `Store.Close`, `Identifier` and `NewIdentifier(Chatter,
  model)`.

**R7b. Tags (extends R6b).**
- The normalizer keeps ':' (identity.go:899), and the filter matches whole
  tags, exactly, after normalization (filter.go:43-56). So `kind:strategy`
  works. A test will pin this.
- `SetTags` works on a committed document without re-ingesting it, and on a
  path that has not been ingested yet (pending).
- It applies normalization only. The model-tag rules do not apply: at most 40
  characters and 3 words, 3–5 tags, and a closed role vocabulary
  (identity.go:854-889).

**R7c. Branch writes copy the document up: DEFERRED (Carl, 2026-11-08).**
`SetDocumentFields` (docfields.go:94) fails on a branch for a document that is
only in the parent: "no document with path". The failure is loud, so nothing
breaks silently. Branches are for the code index, and tags are for memory, so
dund has no branch edit yet. Copy-up is built together with branch merge
(P6). `SetTags` on a branch has the same rule: it works on a document already
in the branch layer and fails loudly otherwise.

**R7d. Nothing left behind after a remove.** Fragments, vectors, FTS (through
the `fragments_ad` trigger), media, doc_fields, notes and shingles all
cascade from documents (schema.sql:68-527). `page_readings` keys on a path
with no foreign key (schema.sql:201) and is not deleted. Fix: delete it in
`DeleteDocument`, with a test that counts every table's rows after a remove.
Originals stay on disk for the prune. Pin `Prune`, so dund can collect them.

## R8: for the code index (APPROVED 2026-11-08; BUILT in v0.7.0)

Status: done on branch v070 (shas from `git log main..HEAD`; merge commits
omitted).
- Line ranges, stable ids (R8a, R8b): 9f768c6, review fix e753416.
- Usage hook (R8c): 95793d9, review fix c10b631.
- Priority and 429 ticket: no code, as decided.
- Branch merge (P6): not built, as decided.
- `api_test.go` and the package "Embedder surface" comment pin all of it
  (the commit that adds this note).

- **Line ranges.** Fragments have byte offsets `start_off`/`end_off`
  (schema.sql:75), set only on the text-overlap path. Neither lines nor offsets
  reach `Fragment` or `Hit`. Add `StartLine`/`EndLine` to `Fragment` (set by
  the caller, or computed from the offsets at commit) and `Hit`. Code
  segmentation: see "R8: code segmenter" below.
- **Stable fragment ids.** `Hit.ID` is a rowid. A real re-ingest deletes and
  re-inserts every fragment (pipeline.go:957-965), which gives new ids. Add
  `fragments.content_id = sha256(path, symbol path, text)`, plus an
  occurrence number when that still collides, for example repeated
  boilerplate in one file. It is the same for unchanged text across
  re-ingests. Add `Hit.ContentID` and
  `Store.FragmentExists(contentID)`.
- **Priority: no raglit change (Carl, 2026-11-08).** Search does not wait on
  ingest: under WAL, readers do not block, and `commitDoc` is one short
  transaction (store.go:205-246; pipeline.go:938). dund keeps a bulk code
  ingest from starving a memory ingest by running two Runners: one over
  memory and strategies, one over code and docs. The shared `Channels` limit
  model capacity across both. A reembed is not a queued job:
  `BackfillVectors` runs outside the queue.
- **Usage hook.** agentkit's `llm.Client.OnUsage` exists (agentkit
  client.go:617), but raglit never sets it. Add `Models.OnUsage func(role,
  model string, u llm.Usage)` and wire it into each role's client. dund then
  enforces per-role daily budgets.
- **429 ticket.** Embed, vision, segment and identity use agentkit's llm
  client, which already sends `X-Corrallm-Request-Id` on retry (agentkit
  client.go:974). STT (stt.go:261), image embed (imageembed.go:49) and the OCR
  engine (ocrengine.go:197) use plain HTTP clients without it. Not needed by
  dund now (it uses no audio or image embedding). They stay as they are until
  something needs them.
- **Branch merge (P6):** when sub-agent or worktree branches of the code index
  arrive. Not before.

**Not needed (dun-bb):** export/import, an ANN index, Postgres, Registry, or
attestation in the core.

## R8: code segmenter (APPROVED 2026-11-08; BUILT in v0.7.0)

Status: done.
- Core: `KindCode`, `CodeSegmenter`, `CodeSpan`, `Models.Code`, the code
  fragmenter, recipe hash with `Runtime()` (R8d): b4d2594, review fix 78e6256.
- raglitd adapter over poly-lsp-mcp v0.5.0 `segment` (R8e): 186ba13, doc fix
  7cfde84.
- The switch is `code_segmenter` in config or `--code-segmenter` on the
  daemon. Default off: `Models.Code` is nil, fragments and recipe hash are
  unchanged.
- Binary size, `cmd/raglit` built with `go build` on the same machine: 54.8 MB
  before the adapter (78e6256), 69.1 MB with it (+14.2 MB, +26%). The cost is
  the gotreesitter grammar blobs.
- Found while building: `CodeSegmenter` gained `Sections` (Markdown
  headings) and `Runtime` (recipe key) beyond the interface sketched below.
  A file indexed before the switch is on keeps its windows: the unchanged-bytes
  fast path skips it before the pool key is checked. `raglit ingest --fresh`
  re-cuts it.
- Root deps test unchanged: root imports no parser.

What exists. poly-lsp-mcp's `symbols.FileSymbols(language, content)`
(symbols/filesymbols.go:78) returns every symbol in source order. Each symbol
has its dotted path, class, declaration line range, doc-comment range and
body-start line. It parses with gotreesitter (MIT, pure Go, no cgo) and has
grammars for c, cpp, go, groovy, java, kotlin, markdown, proto, python, sql,
swift and tsx. Its package also imports poly-lsp-mcp's config, internal/git and
yaml.v3, which a segmenter does not need.

**poly-lsp-mcp side** (poly-lsp-mcp's session builds it; this is the request):
- A new package `github.com/iodesystems/poly-lsp-mcp/segment` that imports
  only gotreesitter and the grammars:
  - no config, git or yaml;
  - no LSP and no MCP.
- `segment.Languages() []string` and `segment.ForPath(path) (lang string, ok
  bool)`, from the file extension.
- `segment.Sections(content []byte) ([]Span, error)` for Markdown: one span
  per heading section, with its heading path as `Sym`. The docs index is
  mostly Markdown, and today it is cut into windows.
- Report the binary-size cost of the grammar blobs, measured on a small
  program built with and without the package.
- `segment.Symbols(lang string, content []byte) ([]Span, error)`. Here `Span`
  is `{Sym, Class string; StartLine, EndLine, CommentStartLine, BodyStartLine
  int}`, with lines 1-based and EndLine exclusive. It returns an error for a
  language without a grammar.
- `symbols.FileSymbols` is rebuilt on top of it, so the two cannot diverge.
- A test that fails if `go list -deps ./segment` includes config, git, yaml
  or anything MCP.
- Tag it (v0.5.0), so raglit and dun depend on a version.

**raglit core** (no poly-lsp-mcp import):
- `KindCode`: a new DocKind for source files, chosen by extension through the
  embedder's segmenter (`ForPath`). With no segmenter, code stays `KindText`,
  as today.
- A new interface `CodeSegmenter { ForPath(path string) (string, bool);
  Symbols(lang string, content []byte) ([]CodeSpan, error) }` and a new field,
  `Models.Code CodeSegmenter`. nil keeps today's behavior.
- The fragmenter for `KindCode`:
  - one fragment per top-level declaration, including its doc comment;
  - a declaration over the embed limit is split at its nested symbols, then
    by windows;
  - code between declarations (imports, package clause) becomes its own
    fragment;
  - a parse error falls back to windows, recorded in the stage log.
- Line ranges reach `Fragment` and `Hit` (R8 "Line ranges"), so a hit cites
  `path:line`.
- The symbol path is stored with the fragment and returned on the Hit
  (`Hit.Symbol`). Search can then show `Server.Start` and not only the
  lines.
- The recipe hash includes the segmenter's name and version, so turning it on
  re-fragments code through the pool.
- Tests use a fake `CodeSegmenter`. The core's deps test is unchanged.

**raglitd** (cmd/raglit) imports `poly-lsp-mcp/segment` and sets
`Models.Code` to a small adapter. This is the only place raglit depends on
poly-lsp-mcp. **dund** does the same.

**Cost:** raglit's go.mod gains poly-lsp-mcp and gotreesitter. aw4 replaces
both raglit and poly-lsp-mcp with ../ paths, so the first raglit commit with
it, and every later version bump, needs an aw4 `go mod tidy`.

**Order:** poly-lsp-mcp `segment` and its tag first, then the raglit core
(it needs only the interface, so it can start in parallel against a fake),
then the raglitd adapter once the tag exists, then dund.

## Optional, out of scope

- A Postgres backend and a storage interface: not until a second backend is
  needed.
- Branch merge (raglit P6).
- A move operation for an index (Carl 2026-11-08: dund would like one; not
  required). Without it, an index left behind when a project moves or is
  dropped stays on disk unused. That is accepted.
- Moving `acl.go`, `keys.go` and `Registry` out of the root package. They are
  not in dund's way.
