# raglit

A local document RAG index you can stand up in one command. Point it at a
folder, ask questions, or hand it to an agent as an MCP tool. One portable
SQLite file is the whole index — pure-Go, single static binary, no services to
run.

## Quickstart

Three ways to install. Each is one binary: CLI, daemon and MCP server; pure Go,
no C compiler.

| how | gets | updates |
|---|---|---|
| release tarball `raglit_{os}_{arch}.tar.gz` (+ `SHA256SUMS`) from GitHub releases | everything, web UI included | `doctor --json` checks the latest release |
| `go install github.com/iodesystems/raglit/cmd/raglit@latest` | CLI, daemon, MCP; the web UI is a placeholder page (it needs node to build) | `doctor --json` checks the Go proxy |
| `make install` in a checkout | everything; rebuilds itself when the checkout changes | `doctor --json` checks the checkout's upstream |

`raglit doctor` says what this install can do and what would add more: models
per role (embedding, vision, text, summary, audio) and external extractors
(poppler, pandoc, tesseract, …). `--json` prints it for tools; `raglit mcp`
sends the same summary as an MCP log notification (logger `raglit/setup`).

```sh
# build
go install github.com/iodesystems/raglit/cmd/raglit@latest

# 1. per-project setup — pick an OpenAI-compatible endpoint + models (a wizard)
cd my-project
raglit init                # writes ./.raglit/ here

# 2. ingest a folder (code, markdown, text; PDFs get OCR'd)
raglit ingest ./src --wait

# 3. ask (works from any subdirectory — raglit finds ./.raglit)
raglit search "how does the auth token refresh work?"
```

That's it. `init` asks for a base URL + API key and lists the endpoint's models
so you can pick a vision model and an embedding model, plus a **project name**
(defaults to the directory name); everything else uses
sensible defaults (you never pass model flags again). The project name namespaces
this repo's indexes on the shared daemon (below), so two projects both using the
`default` index never collide. When the endpoint reports
capabilities (a corrallm-class server), each pick list is **filtered to the
models that fit the role** — image-capable models for OCR, embedding models for
`--embed` — instead of the whole catalog; a plain OpenAI server shows all
models.

`raglit init` is **project-local**: it writes `./.raglit/` in the current
directory, so each repo or sub-project owns its own index and config. Any
command run inside the tree discovers the nearest `.raglit/` by walking up (like
git), so you can run `raglit search` from a deep subdirectory. With no `.raglit/`
found, commands fall back to `$RAGLIT_HOME`, else `~/local/raglit`. Override the
location anywhere with `--home DIR`.

On success `init` prints the MCP server setup (a `claude mcp add-json` line and a
`.mcp.json` block) plus the ingest/search commands for reference.

> No endpoint handy? Every offline piece works without one:
> `raglit demo` runs a self-contained tour, and text/code ingest needs no model at
> all — the deterministic fragmenter runs fully offline (only OCR and figure
> description need a vision model).

## What it does

- **Ingest** folders, files, or URLs (`file://`, `http(s)://`) — lazily (queued)
  and followed with `--wait`. Each item runs a staged pipeline: a scanned page goes
  img→paged-text (OCR cascade: cheap `tesseract`→gibberish-gate→vision VLM); a
  born-digital PDF page uses its text layer, no OCR. Then **one per-document
  fragmenter choice**: text, code, and cheap-OCR'd pages are split by a
  **deterministic overlapping-window** fragmenter (no model — windows snapped to
  line/paragraph edges, each fragment carrying its source offsets); a document any
  page of which the **VLM transcribed** is **LLM-segmented** as a whole (coherent
  ~500-word units — functions bound with their docs — an open fragment carried
  across page boundaries). The choice is stored per document (`frag_mode` =
  `text-overlap` | `llm-seg`, shown in `list_documents`); window / stride / floor
  are config-tunable and capped by the embed model's probed input limit. Every
  stage and its engine is recorded per job.
- **Mail archives (`.eml`, `.mbox`) are read as PAGES, one per message** — an
  archive is not one document. A 24 MB `.eml` holding a decade of nested forwards
  read as "page 1" is unciteable; read as pages, a quotation lands on *which*
  message. Headers are kept (for mail the routing IS the evidence), enclosed
  `message/rfc822` messages become pages of their own, transfer encodings and
  non-UTF-8 charsets are decoded, and `multipart/alternative` indexes the plain
  text rather than the same words twice. Attachments are **named** with their
  media type and size; set `extract_email_attachments` (project-wide or per index)
  and they are also **extracted byte-for-byte** into
  `<archive>.raglit-attachments/` with a `MANIFEST.md` recording which message,
  sender, date and sha256 each came from — so the survey inside the archive
  becomes an ordinary file the next `sync` indexes, with its chain intact. Off by
  default: one archive can carry 69 files.
- **Figures explained into the index** — while the VLM transcribes a scanned page
  it also **describes each figure/diagram/chart inline** (`[FIGURE: …]`), so a
  diagram becomes searchable text with no extra infrastructure. Each figure is also
  recorded as a media object anchored to the fragment holding it and embedded on
  its own — from the **image** (via `nomic-embed-vision`, which shares
  `nomic-embed-text`'s space so a text query still matches) or, with no image
  embedder, from its **description**. `search_figures` ranks figures directly, and
  `get_document` returns a document's figures alongside its text. (Escalating a
  born-digital page that carries a figure to the VLM is opt-in: `ocr.describe_figures`.)
- **Indexing work is deduped**: the daemon caches each processed document in a
  shared pool keyed by `(recipe, file-hash)` — where *recipe* is the models +
  config that shape the output — so the same file, in ANY index or on a retry,
  is reused (fragments + vectors + page images copied in, mode `pooled`) instead
  of re-running the LLM. Re-indexing under different models is a new recipe, so
  it reprocesses. (Embedded/single-index mode dedups per index by content hash.)
  The pool is bounded but lax by default: it grows freely up to a **byte budget**
  (`--pool-max-bytes`, default 4 GiB — counting cached payloads **and** page
  images) and trims by evicting the **oldest-accessed**
  entries — so merges and retries keep reusing pooled work rather than re-indexing
  it. `--pool-max` (entry cap) and `--pool-ttl` (evict unused; off by default) are
  optional; `POST /api/pool/gc` runs it on demand and `GET /api/pool` reports size.
- **Search** — BM25 (`--mode bm25`), vectors (`--mode vec`), or hybrid RRF
  (`--mode hybrid`; the default on an index with embeddings, else BM25). Each hit
  carries `similarity` (raw cosine, when the vector side ranked it) and
  `matched_terms` / `query_terms` (term coverage, stopwords out), so a caller
  can cut weak hits at a fixed threshold. Optionally scoped to an index and/or
  a **path subtree** (`--path /repo/src/api/` — a prefix match, so hierarchical
  corpora don't need a separate index per directory). Results are precise citations: document → page →
  fragment. `search_figures` does the same over described figures.
- **MCP** — expose the index(es) to any MCP client (Claude Desktop, agentkit):

  ```sh
  raglit mcp
  ```

  Tools: `search`, `search_figures`, `list_documents`, `get_document`, `ingest`,
  `index_status`, `list_indexes`, `ocr`. `search` / `search_figures` take an
  optional `path` prefix. `raglit init` prints a ready-to-paste MCP config (Claude
  Code + generic `.mcp.json`) pinned to this project's `.raglit/`.

  An agent that needs a whole document's text: `search` to find a hit (or
  `list_documents` with a `name` filter to find it by filename), then
  `get_document` with that path (or a unique filename substring) to read the full
  indexed text — per-page plus a joined blob (overlapping fragments reassembled
  exactly once via their offsets), plus the document's figures, with optional page
  range and a `max_chars` cap that bounds the whole response (pages included).
  `ocr` is the other read path: it extracts text from a file/URL you supply
  directly (not from the index).

## Commands

```
raglit init                          configure endpoint + models (wizard)
raglit ingest TARGET... [--wait]     queue folders / files / URLs (lazy; --wait follows the daemon)
raglit search "query" [--mode M] [--path P]  M = bm25 | vec | hybrid; P = path-prefix scope
raglit status                        documents/fragments, queue progress, rate, ETAs
raglit identify [DOC...] [--list]    what a document IS: caption, summary, kind (never renames)
raglit prune [--index N] [--yes]     documents whose local file is gone (already out of search); --yes removes
raglit mcp                           stdio MCP server (old name: serve)
raglit daemon                        HTTP API + workers + review UI at /
raglit review                        the daemon, framed as the status/job/OCR review UI
raglit demo                          offline, self-contained tour
```

`--home DIR` overrides the index home (default: nearest `./.raglit` walking up,
else `$RAGLIT_HOME`, else `~/local/raglit`); `--index NAME` selects a named index
within it. With no `--index`, commands use the config's `default_index` (set in
the wizard), falling back to `default`. On the shared daemon, `--index NAME` is
this project's own index; `--index <namespace>:<index>` addresses a reachable
namespace (your project or a `shared` one).

## Configured sources (`raglit sync`)

Instead of passing paths every time, declare source roots + rules in the project's
`.raglit/config.json` and run `raglit sync` — it resolves them to files and
enqueues each (the content-hash dedup skips unchanged ones, so re-syncing is
cheap). Rules layer **project → index → root** (ignore is unioned and always wins;
include is overridable per root), each root's **`.gitignore` is honored**, and a
built-in default drops dot-dirs / `node_modules` / `vendor`. Multi-index is native.

```jsonc
{
  // ...endpoint + models...
  "ignore":    ["**/*.min.js"],     // project-scoped default excludes (this config only)
  "gitignore": true,                 // honor each root's .gitignore (default)
  "indexes": {
    "code": {
      "roots":   [".", "../shared-lib"],
      "include": ["*.go", "*.ts", "*.py", "*.md"],   // a file must match one
      "ignore":  ["*_test.go", "gen/**"]              // merged with project + built-in
    },
    "docs": { "roots": [ { "path": "./docs", "include": ["*.md", "*.pdf"] } ] }
  }
}
```
```sh
raglit sync                       # ingest every configured index's roots
raglit sync --index code --dry-run  # preview one index's matched files
```
Globs: no `/` matches the basename (`*.go`); with `/`, the path (`gen/**`, `**/x`).
`sync` routes to the daemon when `daemon_url`/`--daemon` is set, else the local home.

**Watch** (`"watch": true`): register a project and the daemon keeps its roots
fresh — on an interval it re-plans the sources (same rules) and re-ingests changed
files, dropping documents whose source file was deleted. `raglit sync` auto-
registers when `watch:true`; manage it with `raglit watch`:

```sh
raglit watch          # register this project (idempotent)
raglit watch list     # what the daemon is watching (project, file count)
raglit watch stop     # unregister this project
```
It's a poll (default every 5s, `--watch-interval`), so an unchanged file that
slips through is a no-op thanks to the content-hash dedup; registrations persist
under the daemon root and reload on restart.

## Daemon mode

**By default every client — `mcp` and the CLI (`ingest`/`search`/`status`/
`sync`) — talks to a single shared per-user daemon, auto-starting it if none is
running.** There is no in-process mode: N sessions each opening the index would
be N processes on one SQLite file, running their own workers and calling the LLM
independently → write contention + duplicated indexing work, and every one of
them skipping auth. One daemon (single writer + worker pool + LLM caller, scoped
storage, shared dedup pool) is the only path. `raglit demo` and the tests start a
throwaway daemon of their own.

```sh
# nothing to start — the first client brings the daemon up (at 127.0.0.1:7420,
# storage under $RAGLIT_ROOT / ~/.raglit), and every session connects to it:
raglit ingest ./my-project
raglit search "rollback procedure"
raglit mcp                         # MCP over the shared daemon

# run it explicitly (foreground) if you prefer, or point at a remote one:
raglit daemon --addr 127.0.0.1:7420    # workers + HTTP API + review UI + OpenAPI + GraphQL
raglit search --daemon http://host:7420 "…"   # or RAGLIT_DAEMON / config daemon_url
raglit daemon --stop                    # signal the running daemon to shut down
```

Because that one daemon serves every project, each client namespaces its indexes
by the config's **`project`** name: the daemon index is `<project>__<local>`, and a
project's "search all" is scoped to `<project>__*` — so two repos both using
`default` don't share storage, and neither sees the other's documents. The project
name is **required** to start a daemon-routed client (`mcp` or CLI); `--project`
overrides it. A client with no project still reads (by directory scope or by
name) but does not write. The `<project>__` prefix is internal — search/status/list show plain local names.

**Shared docs** (`shared`): common material — a home `~/doc`, a team handbook — is
indexed **once** under its own project (say `shared`), and other projects opt into
reading it by listing that namespace in their config. A project's "search all"
then spans its own indexes **plus** each shared namespace, and shared hits keep
their `shared__` tag so you can see where they came from. No duplication per
project. Address one specific index in a reachable namespace with
**`--index <namespace>:<index>`** (e.g. `--index shared:handbook`) — for reads and
for writes (so a project can contribute to a `shared` corpus); a bare `--index`
name is always this project's own, and an unreachable namespace is refused (writes
error, reads return nothing). Branches stay project-only.

```jsonc
// ~/doc, indexed once:  raglit --home ~/.raglit-shared ingest ~/doc   (project "shared")
{ "project": "alpha", "shared": ["shared"], "daemon_url": "http://127.0.0.1:7420" }
// `alpha` now searches alpha__* + shared__*; a project without "shared" stays isolated.
```

On startup the daemon records `<root>/daemon.json` (`{pid, addr, root, ...}`) and
removes it on clean shutdown. Clients read it to **discover** the daemon's real
address — so one on a non-default port is found instead of a duplicate being
spawned on 7420 — after verifying the pid is alive and it answers `/api/health`
(a stale file is ignored). `raglit daemon --stop` reads it to signal that pid.

`raglit daemon` is a multi-protocol server (huma + gwag/gat): the same operations
are REST + in-process GraphQL (`/graphql`) + gRPC off one port, with **OpenAPI at
`/openapi.json`**. Storage is **scoped per index** under `--root` (default
`~/.raglit`, so each index lives at `~/.raglit/indexes/<name>/`); `--home DIR`
selects a single-index layout instead. `mcp` becomes a thin **client** to the
daemon when `daemon_url`/`--daemon` is set, so many `mcp` instances share
one daemon.

**Branches** (copy-on-write, worktree-style): `POST /api/branches {name,parent}`
forks a branch whose reads overlay the parent at document grain (writes/deletes
touch the branch only); `GET /api/branches` lists them with age + last-access;
`DELETE /api/branches?name=` drops one.

## Review UI

The daemon also serves a self-contained web UI at `/` — status, job control, and
OCR review. `raglit review` is the same server with a friendlier banner:

```sh
raglit review --addr 127.0.0.1:7420        # then open http://127.0.0.1:7420/
```

- **Status** — documents, fragments, and live job counts (done/running/pending/
  failed) + throughput, auto-refreshing.
- **Job control** — the full ingest queue as a table; **retry** an errored or
  done job (requeues it) and **cancel** a pending one. Each job shows a **mode**
  badge (`text-overlap` = deterministic windows, `llm-seg` = a VLM-transcribed doc
  segmented by the model, `pooled`/`unchanged` = reused/skipped) and expands to its
  **pipeline stages** — the series of tasks it ran: fetch → extract → [ocr] →
  segment → [embed] → commit, each tagged with the engine that handled it
  (text-layer, pandoc, tesseract, vision, llm, text-overlap…), so a failure shows
  exactly which stage broke.
- **OCR review** — pick a document to see its pages: the saved page image beside
  the indexed text, an engine badge per page (**text** = born-digital/plain,
  **vision** = the VLM OCR'd it), and a **Re-OCR (cascade)** button that reruns
  the cheap→gate→VLM cascade on that page's image to show the raw transcription
  and which engine handled it.

Ingest records per-page provenance (engine + page image) under `<home>/pages/`;
documents indexed before this feature show no OCR pages until re-ingested.
Control-plane routes live under `/api/*`.

**Auth: never open.** Every route except `/api/health` needs a bearer token,
on every bind, loopback included. With no OIDC issuer configured the daemon
runs in admin-token mode: it writes `<root>/admin.token` (0600) and accepts
that token. With `auth.issuer` set it also accepts OIDC tokens. A client sends
`$RAGLIT_TOKEN`, else the output of `$RAGLIT_TOKEN_COMMAND` (or config
`auth.token_command`), else, for a daemon on a loopback address, the admin
token file that daemon's `/api/health` names; so on a single-user machine
nothing needs configuring. A browser signs in with `raglit service login-admin`.

## For agents (agentkit)

raglit's `search` output is exactly the shape agentkit's `ragnotify.ParseHits`
consumes, so one `raglit mcp` drives both a model's explicit searches **and**
agentkit's proactive "live-watch" pings (the finder scopes which indexes it
watches via `Opts.ExtraArgs {"index": ...}` — all by default). raglit's
`agent.DocFinder` also plugs straight into a Session's `FinderPreparer`.

## How it's built

- **SQLite FTS5** gives BM25 + the document:page:fragment index in one pure-Go
  dependency (`modernc.org/sqlite`, no CGo). Vectors are stored as BLOBs, cosine
  brute-forced (fine for a local corpus).
- **Two fragmenters, chosen by whether a model is already in the loop.** Text,
  code, and cheap-OCR'd pages take a deterministic overlapping-window splitter with
  exact source offsets — no model, and overlap gives every hit its surrounding
  context (get_document reassembles the document once despite the overlap). A page
  a VLM transcribed is instead **LLM-segmented** (via
  [agentkit](https://github.com/iodesystems/agentkit)) with a schema-validated
  fix-loop and a safe fallback, an open fragment carried across boundaries. Figures
  the VLM sees are described inline and embedded (image or description) for
  `search_figures`.
- **Multi-index** — one home holds several named indexes; `mcp` searches all
  (RRF-merged, tagged), a scoped subset, or a path subtree.

## Home layout

```
./.raglit/                 (per-project; or $RAGLIT_HOME / ~/local/raglit / --home)
  config.json              endpoint + model settings (from `raglit init`)
  index.sqlite             the default index (index-<name>.sqlite for others)
  uploads/                 uploaded documents (the index is their only home)
  pages/                   page images for OCR
```

Everything raglit holds for an index is under one directory — copy it, back it
up, or delete it wholesale. A local-disk document is read from its file and not
copied: when the file is gone, the document is out of search and `raglit prune`
removes it.

## Roadmap

- ◻ Vector reranking; opt-in summaries for oversized fragments.
