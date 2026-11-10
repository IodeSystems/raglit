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
  daemon. Default off: `Models.Code` is nil and no file routes to
  `KindCode`. Text files still get line spans in v0.7.0, and the pool key for
  the text kinds changes once (`|lines=1`, worker.go `poolRecipe`), so a
  changed text file, or a text file added to another index, is fragmented and
  embedded once more. Unchanged files are skipped before the pool is checked.
- Binary size, `cmd/raglit` built with `go build` on the same machine: 54.8 MB
  before the adapter (78e6256), 69.1 MB with it (+14.2 MB, +26%). The cost is
  the gotreesitter grammar blobs.
- Found while building: `CodeSegmenter` gained `Sections` (Markdown
  headings) and `Runtime` (recipe key) beyond the interface sketched below.
  A file indexed before the switch is on keeps its windows: the unchanged-bytes
  fast path skips it before the pool key is checked. `raglit ingest --fresh`
  re-cuts it.
- The root deps test now also bans poly-lsp-mcp and the tree-sitter modules;
  the root package imports no parser.

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

## R10: tree indexing, watching, worktree branches, content-addressed fragments (PLAN FOR REVIEW, 2026-11-09)

Carl's requests, relayed by dun-bb: these go into raglit core so that
raglitd and dund share them. dund keeps its interim `rag_index` until they
land. dun-bb will forward the API shape its sub-agent wanted while it built
dund's interim version; this plan is reconciled against that when it arrives.
`EnqueueMany` (fbb99de, local main, not released) stays; tree indexing is built
on it.

### What exists (read 2026-11-09)

- **Selection rules: in the core.** `PlanSources(cfg, baseDir)` (sources.go:106)
  uses `git ls-files` in a git work tree and a directory walk otherwise. It
  applies include globs plus the built-in, config, index and root ignore globs,
  and already ignores `.claude/worktrees` and `.dun/worktrees`
  (indexcontrol.go:62). `Enqueue` refuses ignored paths and sidecars. The
  worker refuses binary content (worker.go:313) and minified content
  (extract.go:307). There is no size limit.
- **Hash skip: in the core.** The worker skips a file whose bytes match
  `DocumentHash` (worker.go:331).
- **`sync`, the watcher and removal: in cmd only.**
  - `sync` (cmd/raglit/sync.go) enqueues the plan, with no removal and no
    counts.
  - The watcher (cmd/raglit/watcher.go) polls every 5 s by mtime, enqueues
    changed files and calls `RemoveDocument` for files that disappeared. It has
    no fsnotify and no debounce.
- **Not in raglit anywhere:** retrying a failed file only after it changes.
  That logic exists only in dund.
- **Branches.** A branch is its own SQLite file in its own scoped Home
  (`branch.json`), layered over its parent at document grain with tombstones.
  - `ReapSessionBranches` reaps by last access (registry.go:448). Nothing reaps
    a branch whose root is gone.
  - A branch ingest of a file identical to the parent's stores a full copy,
    because `DocumentHash` does not read through the parent (store.go:143).
  - There is no git worktree detection.
- **Reuse.** The pool reuses whole documents keyed by (recipe, file bytes)
  across indexes, so same bytes at a new path cost nothing (pool.go:20-27).
  There is no reuse at fragment grain.
  - `content_id` hashes the path, so it changes on a move (contentid.go:30).
  - The embedded input is the fragment text alone, plus the model's fixed
    document prefix (pipeline.go:1203, embed.go:54). Identical text therefore
    gives an identical vector within one embed model.
- **Two tables reference `fragments(id)`:** `fragment_vectors`
  (schema.sql:141) and `media.fragment_id` (schema.sql:339). FTS follows
  fragments through triggers.

### R10a. Tree indexing in the core (additive)

```go
type TreeOptions struct {
    Include, Ignore []string // on top of the built-in rules
    Gitignore       *bool    // nil = on
    MaxBytes        int64    // 0 = a default (proposed 2 MiB); larger files count as Skipped
    Route           func(path string) (index string) // optional: code vs docs, per caller
    Paths           []string // optional: check only these (from WatchTree); empty = whole tree
}
type TreeResult struct{ Queued, Current, Removed, Failed, Skipped int; Errors []error }
func IndexTree(ctx context.Context, st *Store, root string, o TreeOptions) (TreeResult, error)
```

- **Selection:** the `PlanSources` rules move behind it unchanged (git
  ls-files, globs, worktree ignores). The size limit is new.
- **Classifying files:**
  - Current: the stored hash equals the file's hash.
  - Queued: new or changed, enqueued with one `EnqueueMany`.
  - Removed: an indexed document under `root` that is no longer selected, gone
    through `RemoveDocument`.
- **Failed:** a new table `failed_sources(path, hash, at, error)`, written when
  a job ends in error. A failed file is requeued only when its hash changes.
  This uses the hash, not mtime, so a `touch` or a checkout of the same bytes
  does not retry it.
- **Routing to more than one index** (dund's code vs docs):
  `IndexTrees(map[index]*Store, root, o)` routes by `o.Route` and returns
  per-index results.
- **raglitd:** `sync` and the watcher's reconcile call `IndexTree`, so
  raglitd gains counts and failed-file tracking. One behavior change: `sync`
  now also removes documents whose files are gone. Today only the watcher
  removes them.

### R10b. Watching in the core (adds a dependency)

The API follows the shape dund's builder asked for (dun-bb, 2026-11-09). The
watcher reports changes; indexing them is a separate call.

```go
type TreeRules struct {           // shared by IndexTree and WatchTree
    Include   []string
    Gitignore *bool               // nil = on
    Skip      func(path string) bool
    MaxBytes  int64
}
type WatchOptions struct{ Quiet, MaxDelay, Idle time.Duration }
type Changes struct{ Paths []string; Degraded bool } // one per burst
func WatchTree(ctx context.Context, root string, r TreeRules, o WatchOptions, fn func(Changes)) (*TreeWatcher, error)
func (w *TreeWatcher) Rescan()    // report everything selected, as one burst
func (w *TreeWatcher) Touch()     // resets the idle timer; sessions call it
func (w *TreeWatcher) Stop()
```

- **Events:** fsnotify (BSD-3) watches the selected tree recursively, adding
  directories as they appear.
- **Debounce:** a quiet period (proposed 500 ms) plus a maximum delay
  (proposed 5 s). A save storm or a git checkout becomes one `Changes`
  callback, filtered by the same rules as `IndexTree`.
- **Watch limit:** when the OS limit is hit (ENOSPC on Linux), it logs once
  and sends `Changes{Degraded: true}`. From then on the caller decides: call
  `Rescan()` on its own trigger, as dund does at session start and turn end,
  or poll.
- **Idle:** with `Idle` > 0, the watcher stops itself after that long without
  a `Touch` or an event.
- **Typical use:** `fn` calls `IndexTree` (R10a). Pass the changed paths and
  it checks only those, falling back to the whole tree on `Degraded`.
  dund's `codeLink.reindex` (dun codesearch.go) is the call this replaces.
- **raglitd:** its project watchers switch to `WatchTree`. Polling remains
  only as raglitd's own fallback on `Degraded`. This changes how raglitd
  watches: a change is seen within ~0.5 s instead of within 5 s, and it costs
  inotify watches.

### R10c. Content-addressed fragments (schema redesign; breaking)

- **Fragment key:** `sha256(text)`. The text is exactly what is embedded (the
  document prefix is fixed per model), so equal text means an equal vector.
  The symbol is left out of the key on purpose: identical text under two
  symbols shares one embedding, and the symbol is a property of the location.
- **`fragments(id, key UNIQUE, text)`** holds the text, the FTS row and the
  vector, each once.
- **`locations(id, doc_id, fragment_id, page, ord, start_off, end_off,
  start_line, end_line, symbol, loc_id)`** holds everything that is per
  document today. `loc_id` is the R8 content id (`sha256(path, symbol, text)`
  plus the occurrence number), so `Hit.ContentID` keeps its meaning.
- **Commit:** `commitDoc` diffs the document's old locations against its new
  ones. Only fragments whose key is new are inserted, FTS-indexed and
  embedded. A copy, a vendored file or a rename adds location rows only.
- **Search:** rank fragments with FTS, vector and RRF exactly as today, then
  expand each hit to its live locations. Path, tag and field filters and the
  branch overlay apply to locations, so a fragment whose only locations are
  filtered out does not match. A filter is a join against locations inside
  the same query, so it still applies before ranking.
- **Hit:** one Hit per fragment, with `Locations []Location`. dund prefers
  this shape. The existing Path, Page, Ord, StartLine, EndLine, Symbol and
  ContentID fields carry the best location: the first in the overlay order,
  then by path. raglitd's output keeps today's one-row-per-hit shape from
  those fields.
- **GC:** removing a document drops its locations. A fragment with no live
  location is deleted along with its FTS row and vector, in the same
  transaction.
- **`FragmentExists(locID)`** means "this location is live", through the
  overlay. A new `FragmentKeyExists(key)` means "the text is indexed
  somewhere".
- **Media/figures:** `media.fragment_id` points at a location, not at a
  fragment, because a figure belongs to a place in a document.
- **Pool:** stays at document grain across indexes. Within an index, reuse is
  native.

### R10d. Worktree branches (on R10c)

- **Storage options:** dun-bb's sketch puts a branch column on locations.
  With today's one-file-per-branch layout, a branch's locations would point at
  fragments in its parent's file. Keys are content hashes, so that works, but
  every search then joins two databases.
  - Option 1: keep one file per branch. Branch locations reference fragments
    by key; a fragment not in the parent is stored in the branch file. Search
    queries both files and merges.
  - Option 2 (recommended): one database per index. A branch is a row in
    `branches(id, name, parent_id, root, created_at, last_access)`, and
    locations and tombstones carry `branch_id`. One FTS table, one vector
    table, one writer. The overlay becomes a visibility rule on locations: a
    document's locations from the nearest branch that has the document, with
    tombstones hiding the parent's. A branch costs rows, not a file. Reaping a
    branch deletes its rows, then the fragment GC runs.
- **Worktree detection:** `WorktreeOf(root) (mainRoot string, ok bool)`
  through `git rev-parse --git-common-dir`. A worktree's index is a branch of
  the main checkout's index. `IndexTree` on a branch:
  - stores nothing for a file whose bytes equal what the parent shows, since
    hashes now read through the overlay;
  - adds locations for a file that differs;
  - tombstones a file deleted in the worktree.
- **GC:** `ReapBranches` drops a branch whose `root` no longer exists, in
  addition to the existing idle-time reaping of session branches.
- **API (dund's shape):**
  - `OpenBranch(parent Home, name, root string) (*Store, error)`. It is
    idempotent: it opens an existing branch or creates one. Writes go to the
    branch only, and deletions are tombstones.
  - `GCBranches(alive func(root string) bool) (removed int, err error)`: the
    caller decides whether a root still exists.
  - `BranchDiff` stays: it compares by content hash. With R10c it becomes
    "add or tombstone locations".
- **Tags:** `SetTags` on a parent-only document keeps failing. Tree indexing
  never calls it, and copy-up waits for P6. The failure becomes a typed error,
  `ErrParentOnly` (`errors.Is`), in v0.8.0. dund asked for that in place of a
  string.

### Order and versions

1. **R10a, R10b and `ErrParentOnly`** are additive. raglitd gains removal in
   `sync` and the switch to fsnotify. Release as **v0.8.0**.
2. **R10c, then R10d**, are breaking: the schema, the `Hit` shape and branch
   storage change. Release as **v0.9.0**.
3. **Step 0 before R10c:** a migration prototype on a copy of a real raglitd
   index, which measures the dedupe ratio and the migration time. If the
   dedupe ratio is near 1 for document corpora, R10c's benefit is mostly
   code. That would argue for a smaller version.

### Risks

- **R10c touches the hottest code in raglit:** commit, search, the overlay,
  figures, identity and fields fragments, collapse and the pool. It is the
  largest change since the daemon.
- **Migration:** existing indexes are rewritten in place (dedupe by key, the
  locations built from today's fragments, existing vectors kept). This must
  be resumable and logged, like the R8 backfill. It is not measured yet.
- **The one-database branch model (option 2) replaces how raglitd stores
  branches.** Existing session branches are short-lived (TTL-reaped), so they
  are dropped rather than migrated. Named branches are migrated.
- **Watching:** fsnotify on large trees can hit inotify limits. The poll
  fallback covers that, but changes are slower there.

### Blocking decisions (Carl)

1. Fragment key: `sha256(text)`, with the symbol on the location
   (recommended), or `sha256(text, symbol)`?
2. Branch storage: one database per index with a branches table (option 2,
   recommended), or one file per branch with cross-file references by key
   (option 1)?
3. Existing indexes: migrate in place (recommended), or require a re-index?
4. raglitd: switch its watcher to fsnotify with the poll as fallback
   (recommended), and have `sync` remove documents whose files are gone
   (recommended)?
5. Releases: tree indexing and watching first as v0.8.0, then content
   addressing and worktree branches as v0.9.0 after the step-0 measurement?

## R11: worktree branches on today's storage (APPROVED, Carl 2026-11-09)

Carl: branches and worktrees come first, as the most cost-effective part of
R10. They are built on today's storage, the per-file branch with a
document-grain overlay, without waiting for content addressing (R10c). A
worktree's index stores copies only of the files that differ from the main
checkout, plus tombstones for files deleted in the worktree. This is most of
the saving, at a fraction of R10c's cost. R10a, R10b and R10c stay planned
and unbuilt.

### The problem today's overlay does not solve: paths

A worktree's files live at different absolute paths from the main checkout's
files (`<main>/a.go` vs `<worktree>/a.go`). The overlay shadows by path, so as
it stands every worktree file looks new and nothing in the parent is ever
shadowed.

The fix: a branch records its **root**, and the parent's root is known. The
overlay translates paths at the boundary.
- Reading through to the parent (`DocumentHash`, `Documents`, search hits,
  `DocText`, `FragmentExists`, `Tags`): a branch path under `branchRoot`
  becomes `parentRoot + rel` for the lookup, and parent results come back
  rewritten to `branchRoot + rel`.
- Shadowing and tombstones are keyed by the branch path.
- A branch with no root (today's session branches) does no translation, so
  existing behavior is unchanged.
- Limitation: `Hit.ContentID` for a hit served from the parent is the
  parent's id, because it hashes the parent's path. `FragmentExists` through
  the branch accepts it. Content ids that survive a move need R10c.

### What gets built

- **`BranchMeta` gains `Root` and `ParentRoot`.**
  `OpenBranch(parent Home, name, root, parentRoot string) (*Store, error)` is
  idempotent: it opens an existing branch home or creates one. The branch home
  is a directory chosen by the caller (`Home(dir)`), not the Registry. Writes
  go to the branch, and deletions are tombstones (`DeleteDocument` already
  does this).
- **`DocumentHash` reads through** the parent with path translation. The
  worker's unchanged-bytes skip (worker.go:331) then skips a worktree file
  whose bytes equal the parent's, so nothing is copied.
  - Caveat: if the main checkout later changes that file, the branch still
    shows the parent's new version until the branch's next index sees the
    mismatch and stores its own copy. That is correct, because the worktree's
    file then differs.
- **`Documents()` lists parent documents under branch paths.** A caller's
  tree indexer (dund's, or `IndexTree` later) can then see a file deleted in
  the worktree and call `RemoveDocument`, which tombstones it.
- **Search, filters and listings** translate paths: FTS, vector, hybrid, the
  filtered variants, and a path-prefix filter given in branch paths.
- **`WorktreeOf(dir) (mainRoot string, ok bool)`** uses `git rev-parse
  --git-common-dir`, or reads the `.git` file. It is a helper; the caller
  decides which index is the parent.
- **`GCBranches(dirs []Home, alive func(root string) bool) (removed int, err
  error)`** removes branch homes whose root is gone. The caller lists its
  branch homes, because raglit does not own dund's layout.
- **`ErrParentOnly`** (`errors.Is`) replaces the string error for `SetTags`
  and field writes on a parent-only document.
- **Pins** in api_test.go for all of the above.
- **raglitd:** no behavior change. Its Registry branches have no root.

### Risks

- Path translation has to be applied at every overlay boundary. Missing one
  shows the parent's path or misses a shadow. The tests have to cover every
  overlay read: search in all three forms, filtered, Documents,
  MatchDocuments, DocText, DocumentHash, FragmentExists, Tags and BranchDiff.
- Two worktrees of one main checkout are two branches of the same parent.
  That needs no extra handling, but it gets a test.

### Release

This ships with `EnqueueMany` (fbb99de) as **v0.8.0**.

## R12: cut model cost on edits (APPROVED, Carl 2026-11-09)

Carl's priority, relayed by dun-bb: a minor edit must not cost model calls
for nothing.

### What a one-line edit costs today (read in the code)

- **Every fragment is embedded again.** `commitDoc` deletes and re-inserts
  all of the document's fragments, and the embedder runs over all of them
  (pipeline.go:1017, 1203). Nothing reuses a vector for unchanged text.
- **One identity call.** Any change to the text queues a full re-caption
  in the same transaction (pipeline.go:1161), whatever the size of the change.
- **The segmenter is free for text and code.** It uses the overlap windows or
  the tree-sitter code segmenter. The LLM segmenter runs only on pages that
  escalate to the vision model (scans and images), and those are re-read only
  when their bytes change.

So for dund's code, docs and memory indexes, the waste is the
**re-embedding** and the **re-caption**. Both can be fixed without the
content-addressed store (R10c).

### R12a. Reuse vectors by text (no schema redesign)

- Add `fragments.text_key = sha256(text)`, indexed.
- Before embedding, look up an existing vector for each new fragment's
  `text_key` in the same index and embed model. That covers this document's
  old fragments (read before they are deleted), other documents, and the
  parent of a branch. A hit copies the vector and costs no call.
- Only text that is actually new gets embedded.
- Storage still holds one vector row per fragment. Only the embed calls are
  saved, and the calls are what costs.
- With R11, a worktree branch then spends model calls only on its changed
  text. That meets dun-bb's item 2 ("a worktree costs only its changed text")
  for cost, without R10c. R10c would also save storage. It is deferred.

### R12b. Delta report before any spend

- Segmentation for text and code is local, so the delta is known before any
  model call. At commit, compare the new fragments with the stored ones by
  `text_key`:
  - **unchanged:** same text, same order;
  - **moved:** same text, different order or lines;
  - **added**, **removed**.
- **Damage** = (bytes of added text + bytes of removed text) / (bytes of the
  document before the edit), counted in bytes. Bytes are used, not a fragment
  count, because fragment sizes vary widely (a one-line gap fragment against
  a 200-line function).
- The delta is stored per document (`doc_deltas`: the latest, plus the
  damage accumulated since the last caption). It is returned by
  `Store.DocumentDelta(path)`, on `JobInfo`, and summed in `IndexTree`'s
  result once R10a exists.

### R12c. Re-caption only past a damage threshold

- A new `Models.RecaptionDamage float64`. When it is > 0, a text change queues
  identity only once accumulated damage reaches the threshold (for example
  0.30), or when the document has no caption.
- Below the threshold, the caption stays. It is marked stale with the damage
  so far, and `DocSummary` carries `CaptionDamage`. Damage accumulates across
  edits, and a re-caption resets it to 0.
- 0 means today's behavior: re-caption on every change.
- A forced re-caption (`EnqueueIdentity(force)`) always runs.

### R12d. Budgets per role

- A new `Models.Budget map[role]Budget{Tokens, Calls int; Period
  time.Duration}`, metered through the same hook that feeds `OnUsage`.
- **Before a job spends:**
  - an embed over budget defers the whole document's job (state `deferred`,
    with its delta and the reason);
  - an identity call over budget defers the identity job.
  
  Deferred work is not dropped. It runs once the period resets.
- **Query embeddings for search are never metered or deferred.**
- `Store.Spend()` reports the spend and remaining budget per role, and
  `Jobs("deferred")` lists what is waiting.
- A nil budget means no limit, which is today's behavior.

### R12e. Throttled tree watcher (R10a + R10b, extended)

The watcher from R10b, plus:
- a **minimum interval per root**;
- **one walk per root** no matter how many callers watch it (single-flight);
- requests merged across callers, through a `WatchHub` keyed by root with
  `Subscribe(root, fn) / Unsubscribe`.

It replaces dund's turn-end re-index. `IndexTree` (R10a) comes with it,
because the watcher's callback is what calls it.

### R12f. LLM re-segmentation of only the changed region

For paged documents that escalate to the LLM segmenter, re-segment only the
pages whose text hash changed and reuse the segments of the other pages.
Whether the current per-page provenance allows this has not been checked.
Investigate before committing to it.

### Releases (proposed)

- **v0.8.0:** EnqueueMany and R11 worktree branches. In progress.
- **v0.9.0:** R12a–e. R12a goes first, because it alone removes most of the
  embedding cost.
- R12f after investigation. R10c (content addressing) is deferred: with
  R12a, its remaining gain is storage, not cost.

### Decisions (Carl, 2026-11-09)

1. **Order:** R12e (the throttled watcher, with IndexTree) and R12a first,
   then R12b–d. R12f only after its investigation. R10c (one fragment, N
   locations) is not dropped: it comes right after v0.9.0.
2. **Damage:** in bytes, reported **per fragment and per document**. A
   fragment's damage is the share of its text that changed (1.0 for added or
   removed). A document's damage is (added + removed bytes) / its bytes
   before the edit, accumulated across edits. dund starts its threshold at
   30%.
3. **Over budget:** commit the free parts and defer the paid ones.
   - Committed now: unchanged and moved fragments, deletions, and new text in
     FTS, so text search stays correct.
   - Deferred: embedding of the new text, and the re-caption. The document is
     marked as pending vectors.
4. **raglitd:** no gating by default. dund turns gating on from its config.

### Decisions (Carl, 2026-11-10), from the v0.9.0 review

1. **Failed sources:** a document error (extract or parse failure,
   unsupported or unreadable content) is final until the file's bytes
   change. Only a transient error backs off and retries, starting at 15 min
   and doubling up to 24 h. Transient means context cancelled or timed out,
   a dial or connection error, HTTP 5xx, or 429. (i_1m70ksc8309va5ek9cbj81)
2. **Daemon start:** `IndexTree` skips hashing a file whose size and mtime
   equal the stored source stat (`RecordSourceStat`). It hashes only files
   whose stat changed. (i_1m70ktazhfvj4xhrq1d38f)
3. **Watcher contract:** `TreeWatcher.Stop` and the hub's unsubscribe only
   signal, and never wait. A new `Wait()` blocks until the watcher, and every
   callback, has returned. The goroutine-id detection is removed.
   (i_1m70kt1yb24q9bf2nna9mn)
4. **Scope:** v0.9.0 is R12e plus R12a. R12g, R12b, R12c, R12d and `NotTags`
   go in v0.10.0.
5. **Packing (R12g):** at fragment level, as planned. A packed fragment is
   still one embedding unit. There is no separate chunk table.

### R12g. Pack small pieces into fragments of a target size (Carl, 2026-11-09)

Carl asked whether small pieces are merged so that damage is not fickle. The
code says (2026-11-09):
- **Text-overlap windows** stay near a target: window 1000 chars, stride 750,
  floor 300. Only the tail under the floor is folded in (fragment.go:27-29,
  98-105).
- **The LLM segmenter** has a real floor and ceiling: `Assembler` MinChars
  3000, MaxChars 9000 (segment.go:391-399).
- **The v0.7.0 code and Markdown path has no floor.** Each top-level
  declaration and each heading section becomes its own fragment, however
  small (codesegment.go fragmentCode). Only pieces with no letters or digits
  are merged. A file of 40 one-line declarations is 40 fragments, so one edit
  changes 1/40 of the fragments but a large share of a small fragment.

Fix: pack adjacent declarations (or sections) into one fragment up to a
target. Use the text window as the target and the floor as the minimum, so a
fragment sits at roughly 750–1000 chars like the text path does.
- A declaration that is too large on its own is still split as today.
- Symbol becomes the list of the packed symbols, so Hit.Symbol stays useful:
  `Symbol` is the first one and `Symbols` the full list.
- Pack boundaries must not cascade. Greedy packing from the top of the file
  shifts every later pack when one function grows, and every fragment after
  the edit then counts as changed. Packing is therefore content-defined:
  - a pack ends at a declaration boundary where hash(declaration text)
    satisfies a condition, with the target size as the expected spacing and
    the floor and ceiling as hard limits;
  - this is the rolling-hash chunking rsync and restic use, applied at
    declaration grain.
  An edit then changes its own pack and at most the next one.
- Markdown sections pack the same way, never across a top-level (`#`)
  heading.
- Ships in v0.9.0, before R12b (damage is computed over packed fragments).

### Small items queued for v0.9.0 (dun-bb, 2026-11-09)

- `SearchFilter.NotTags []string`: whole tags, where any match excludes. It
  is applied before ranking, like Tags. dun keeps unaccepted lessons tagged
  `candidate`, and recall must not return them or let them take up result
  slots.
- Pin `Models.Embedder()` and `Embedder.Client`, or a pinned way to build an
  admitted embedder for ingest and an unadmitted one for queries. Document
  that an embedder built with `NewEmbedder` over a plain client bypasses
  Channels admission.

## Optional, out of scope

- A Postgres backend and a storage interface: not until a second backend is
  needed.
- Branch merge (raglit P6).
- A move operation for an index (Carl 2026-11-08: dund would like one; not
  required). Without it, an index left behind when a project moves or is
  dropped stays on disk unused. That is accepted.
- Moving `acl.go`, `keys.go` and `Registry` out of the root package. They are
  not in dund's way.
