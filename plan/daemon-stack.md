# raglit daemon rebuild — huma + sqlc + metaquery (+ client/daemon split)

Status: in progress (2026-07-23). Decision: FULL huma+sqlc+metaquery stack for
the remote daemon (user-chosen). This is the foundation for icebox items #1
(client/daemon split) and #2 (scoped/branch storage).

## Key finding — FTS5 forces a hybrid (not a choice, a constraint)

sqlc's SQLite parser CANNOT model fts5: a query referencing the `fragments_fts`
virtual table (`… MATCH ?`, `bm25(fragments_fts)`) fails codegen with
`column "fragments_fts" does not exist`. The schema's `CREATE VIRTUAL TABLE` +
triggers parse fine; only fts *queries* break. Therefore:

- **sqlc + metaquery** owns all RELATIONAL CRUD: documents, fragments (insert),
  fragment_vectors (insert), ingest_jobs (+ jobs listing w/ metaquery
  filter/sort/paginate), job_stages, ocr_pages, branches (later).
- **Raw hand-written SQL** stays for **FTS5 MATCH search** and the **vector
  cosine scan** only — a small `search` module on the same `*sql.DB`.

Both share one modernc `*sql.DB`; no CGo (metaquery ships `mqsqlite`, a
`database/sql` adapter documented against `modernc.org/sqlite`).

## Phase status

- ✅ **P0 — client config foundation** (stack-independent): `Config.DaemonURL`;
  `resolveDaemon(flag > $RAGLIT_DAEMON > config)`; `ingest`/`search`/`status`
  route to a daemon from config. So a project `.raglit/` can be a CLIENT config.
- ✅ **P1 — sqlc/metaquery foundation, validated end-to-end**: `sql/schema.sql`
  (canonical schema), `sql/query.sql` (starter CRUD), `sqlc.yaml` (engine sqlite,
  metaquery plugin, `emit_metaquery: cols`), generated `internal/db`; `go.mod`
  require+replace `../sqlc-go-codegen-metaquery`. Runtime proven
  (`internal/db/roundtrip_test.go`): plain typed queries AND a metaquery Builder
  + `ApplyFilters` + `mqsqlite.Scan` run on modernc. Regenerate: `sqlc generate`
  (needs the plugin built: `make -C ../sqlc-go-codegen-metaquery bin/…`).
- ✅ **P2 — gat multi-protocol daemon** (gwag/gat: REST + in-process GraphQL +
  gRPC off one port). `raglit httpd` (`cmd/raglit/httpd.go`, package main so it
  reuses the daemon helpers). chi + humachi + `gat.New`→`gat.Register`→
  `gat.RegisterHuma(api,g,"")`. gwag v1.1.0-rc.7 (replace ../gwag), huma v2.39.0,
  chi v5.3.1; no NATS (embedded mode); gRPC is one extra `gat.RegisterGRPC` call.
  - Endpoints ported (all at parity with the stdlib daemon): health, indexes,
    status, search, ingest, jobs(+retry/cancel), documents, doc(review), reocr,
    find-documents (MCP list_documents), get-document (MCP get_document). HTML
    review UI (/) + binary page-image are plain chi routes.
  - **The existing review UI works UNCHANGED** — paths kept identical
    (/status, /api/jobs, /api/documents, /api/doc, /api/page-image, /search…).
  - Handlers call the existing Store/Registry (P3 swaps Store's guts to
    internal/db under them, no handler change). Verified: httpd_test.go (httptest
    over a real registry — all ops + GraphQL + POST job-control) and live on the
    dogfood index (OpenAPI 13 paths, GraphQL schema auto-gen, search works).
  - Note: huma adds a `$schema` field to JSON bodies (harmless — UI reads named
    fields; CLI structs ignore unknowns). Legacy stdlib `daemon`/`review` still
    present; switch them to `httpd` after P3/P4.
  - Not yet on gat: MCP `ocr` tool endpoint (extract arbitrary bytes) — add with
    P4 serve-parity. `Store.SQLDB()` accessor deferred to P3 (handlers use Store
    methods for now).
- ✅ **P3 — Store internals migrated to internal/db (sqlc + metaquery)**.
  - Schema unified: `store.go` embeds `sql/schema.sql` (`//go:embed`) — one
    source of truth for codegen AND runtime, no drift.
  - `Store.q *gen.Queries` (over `s.db`); `gq(tx)` binds generated queries to a
    transaction. Migrated: queue.go (Enqueue/claimNextJob/complete/fail/Retry/
    Cancel/IndexStatus/recentAvg — **Jobs via a metaquery Builder**: OrderBy +
    optional state filter + pagination), stages.go (InsertStage/JobStages),
    review.go (UpsertOcrPage/Documents/DocReview/PageImagePath), docget.go
    (MatchDocuments; **DocText page-range via a metaquery Builder**), pipeline.go
    `commitDoc` + store.go `Ingest` (generated inserts inside their tx via gq).
  - **Raw SQL remains ONLY** for FTS5 `MATCH`/`bm25` search + the vector cosine
    `SELECT ... fv.vec` (store.go:335/379) — the two sqlc can't model.
  - Verified: full suite + `-race` green; live gat daemon over the migrated layer
    on the 49-doc dogfood index (status/documents/search/get-document all correct).
  - Regenerate: `sqlc generate` (plugin: `make -C ../sqlc-go-codegen-metaquery
    bin/sqlc-go-codegen-metaquery`).
- ✅ **P4 — serve as daemon client** (completes item #1). `serve` now branches on
  `resolveDaemon` (flag > $RAGLIT_DAEMON > config `daemon_url`): with a daemon
  set, the MCP tools proxy to its HTTP surface; else embedded (own the registry).
  - serve.go refactor: tool DEFINITIONS extracted to `addRaglitTools(s, toolHandlers)`
    (one contract); embedded mode supplies local handlers, client mode supplies
    `daemonToolHandlers(url, defLimit)` (serveclient.go). Added a daemon `/api/ocr`
    endpoint + `daemonPostJSON`; `stripSchema` removes huma's `$schema` so proxied
    JSON matches embedded output; `ingest` reshaped to the `{job_id,…}` contract.
  - Verified: serveclient_test.go (client handlers over an httptest gat daemon:
    list_indexes/status/search/list_documents/get_document/ingest) + live MCP
    stdio smoke (`serve --daemon` → all tools return the daemon's data).
- ✅ **P5 — scoped storage** (item #2). `OpenScopedRegistry(root)`: each index is
  its OWN Home under `<root>/indexes/<name>/` (own index.sqlite + originals/ +
  pages/), fully isolated — vs the single-home layout (sqlite siblings) kept for
  embedded/project use. `raglit.DefaultRoot()` = `$RAGLIT_ROOT` else `~/.raglit`;
  daemon config at `<root>/config.json`.
  - `raglit httpd`: `--root` (default DefaultRoot(), scoped) with `--home` as an
    explicit single-home override (back-compat, e.g. the dogfood `.raglit`).
    `openDaemonRegistry` picks the layout; config + workers read from the root/home.
  - Fixed: huma marks body fields required by default → POST /ingest needed
    `title`; `index`/`title` (+ ocr path/data/mime) are now `omitempty`.
  - Verified: registry_test.go (per-index dirs + isolation), httpd_test.go
    (ingest optional fields), and live scoped daemon (proj-a/proj-b/default each
    their own indexes/<name>/index.sqlite; `invoice` isolated to proj-b).
  - Legacy stdlib `daemon`/`review` still single-home (being retired). Client-only
    `init` (write daemon_url, skip local index bytes) still TODO — small UX piece.
- ◻ **P6 — branch storage** (item #3): branch-off-parent (diff layers, COW at
  document grain), delete-branch, list-branches (age + last-access) — needs
  per-branch created_at + last_accessed_at.

## Blocking decisions (user owns)

- gwag/gat (REST+GraphQL+gRPC) vs plain huma REST — leaning plain REST unless you
  want the extra surfaces.
- Scoped-storage root + naming for `~/.raglit/indexes/<index>` and how branches
  map to files (ATTACH vs overlay-in-one-file).
