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
- ◐ **P2 — gat multi-protocol daemon** (DECISION: gwag/gat, not plain huma —
  REST + in-process GraphQL + gRPC off one port, per the other iode services).
  - ✅ skeleton up + proven: `httpd/` package — chi + humachi + `gat.New` →
    `gat.Register` → `gat.RegisterHuma(api,g,"")`. Serves REST `/api/health`,
    OpenAPI `/openapi.json`, GraphQL `/graphql` (httpd/server_test.go via
    httptest). gwag v1.1.0-rc.7 via replace ../gwag; huma v2.39.0; chi v5.3.1.
    No NATS needed (gat embedded mode). gRPC is one extra `gat.RegisterGRPC(
    router,g,"")` after RegisterHuma when wanted.
  - ◻ **next**: port endpoints as gat handlers `func(ctx,*In)(*Out,error)` over
    the Store + `internal/db`: health✓, then indexes/ingest/search/status/
    documents/doc/jobs(+retry/cancel)/page-image/reocr + MCP-parity
    list_documents/get_document/ocr. Jobs/documents lists use metaquery Builders
    (ApplyFilters/Pagination); search uses raw FTS. Expose Store's *sql.DB
    (add `Store.SQLDB()`), share one connection (single writer).
  - **risk**: keep the running stdlib review UI working until the gat daemon
    reaches parity, then switch `raglit daemon`/`review` over.
- ◻ **P3 — migrate Store internals to internal/db** (queue/review/docget/stages
  → generated queries; unify `store.go` schema const with `sql/schema.sql` via
  `//go:embed` — currently DUPLICATED, keep in sync until then). FTS/vec stay raw.
- ◻ **P4 — serve as daemon client**: MCP tools proxy to the daemon over HTTP
  when `daemon_url` set; local/embedded mode is the fallback. Completes item #1.
- ◻ **P5 — scoped storage** (item #2): daemon owns per-index storage under its
  own root (`~/.raglit/indexes/<index>`); client holds config only.
- ◻ **P6 — branch storage** (item #3): branch-off-parent (diff layers, COW at
  document grain), delete-branch, list-branches (age + last-access) — needs
  per-branch created_at + last_accessed_at.

## Blocking decisions (user owns)

- gwag/gat (REST+GraphQL+gRPC) vs plain huma REST — leaning plain REST unless you
  want the extra surfaces.
- Scoped-storage root + naming for `~/.raglit/indexes/<index>` and how branches
  map to files (ATTACH vs overlay-in-one-file).
