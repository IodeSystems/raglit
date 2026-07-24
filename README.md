# raglit

A local document RAG index you can stand up in one command. Point it at a
folder, ask questions, or hand it to an agent as an MCP tool. One portable
SQLite file is the whole index — pure-Go, single static binary, no services to
run.

## Quickstart

```sh
# build
go install github.com/iodesystems/raglit/cmd/raglit@latest

# 1. per-project setup — pick an OpenAI-compatible endpoint + models (a wizard)
cd my-project
raglit init                # writes ./.raglit/ here

# 2. ingest a folder (code, markdown, text; PDFs get OCR'd)
raglit ingest ./src --now

# 3. ask (works from any subdirectory — raglit finds ./.raglit)
raglit search "how does the auth token refresh work?"
```

That's it. `init` asks for a base URL + API key and lists the endpoint's models
so you can pick a vision model and an embedding model; everything else uses
sensible defaults (you never pass model flags again). When the endpoint reports
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
> `raglit demo` runs a self-contained tour, and text ingest falls back to a
> dependency-free splitter when no model is configured.

## What it does

- **Ingest** folders, files, or URLs (`file://`, `http(s)://`) — lazily (queued)
  or with `--now`. Each item runs a staged pipeline: a scanned page goes
  img→paged-text (OCR cascade: cheap `tesseract`→gibberish-gate→vision VLM) then
  paged-text→fragments; a code/text file goes straight to fragments (LLM-segmented
  into coherent ~500-word units — functions bound with their docs — or a
  dependency-free offline split when no model is set); a born-digital PDF page
  uses its text layer, no OCR. Every stage and its engine is recorded per job.
  **Indexing work is deduped**: the daemon caches each processed document in a
  shared pool keyed by `(recipe, file-hash)` — where *recipe* is the models +
  config that shape the output — so the same file, in ANY index or on a retry,
  is reused (fragments + vectors + page images copied in, mode `pooled`) instead
  of re-running the LLM. Re-indexing under different models is a new recipe, so
  it reprocesses. (Embedded/single-index mode dedups per index by content hash.)
- **Search** — BM25 (`--mode bm25`, default), vectors (`--mode vec`), or hybrid
  RRF (`--mode hybrid`). Results are precise citations: document → page →
  fragment.
- **Serve** — expose the index(es) to any MCP client (Claude Desktop, agentkit):

  ```sh
  raglit serve
  ```

  Tools: `search`, `list_documents`, `get_document`, `ingest`, `index_status`,
  `list_indexes`, `ocr`. `raglit init` prints a ready-to-paste MCP config (Claude
  Code + generic `.mcp.json`) pinned to this project's `.raglit/`.

  An agent that needs a whole document's text: `search` to find a hit (or
  `list_documents` with a `name` filter to find it by filename), then
  `get_document` with that path (or a unique filename substring) to read the full
  indexed text — per-page plus a joined blob, with optional page range and a
  `max_chars` cap. `ocr` is the other read path: it extracts text from a file/URL
  you supply directly (not from the index).

## Commands

```
raglit init                          configure endpoint + models (wizard)
raglit ingest TARGET... [--now]      queue folders / files / URLs (lazy; --now drains)
raglit search "query" [--mode M]     M = bm25 | vec | hybrid
raglit status                        documents/fragments, queue progress, rate, ETAs
raglit serve                         stdio MCP server
raglit daemon                        HTTP API + workers + review UI at /
raglit review                        the daemon, framed as the status/job/OCR review UI
raglit demo                          offline, self-contained tour
```

`--home DIR` overrides the index home (default: nearest `./.raglit` walking up,
else `$RAGLIT_HOME`, else `~/local/raglit`); `--index NAME` selects a named index
within it. With no `--index`, commands use the config's `default_index` (set in
the wizard), falling back to `default`.

## Daemon mode

For big or ongoing ingests — or to share one index across many clients — run a
daemon that owns storage, the workers, and the LLM calls; other invocations call
into it over HTTP instead of touching the SQLite files:

```sh
raglit daemon --addr 127.0.0.1:7420        # workers + HTTP API + review UI + OpenAPI + GraphQL

# point any command (or `serve`) at it (or set RAGLIT_DAEMON=http://host:7420 / config daemon_url)
raglit ingest --daemon http://127.0.0.1:7420 ./my-project
raglit search --daemon http://127.0.0.1:7420 "rollback procedure"
```

`raglit daemon` is a multi-protocol server (huma + gwag/gat): the same operations
are REST + in-process GraphQL (`/graphql`) + gRPC off one port, with **OpenAPI at
`/openapi.json`**. Storage is **scoped per index** under `--root` (default
`~/.raglit`, so each index lives at `~/.raglit/indexes/<name>/`); `--home DIR`
selects a single-index layout instead. `serve` becomes a thin **client** to the
daemon when `daemon_url`/`--daemon` is set, so many MCP `serve` instances share
one daemon.

**Branches** (copy-on-write, worktree-style): `POST /api/branches {name,parent}`
forks a branch whose reads overlay the parent at document grain (writes/deletes
touch the branch only); `GET /api/branches` lists them with age + last-access;
`DELETE /api/branches?name=` drops one. Localhost, no auth — don't expose it.

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
  badge (`llm` = LLM-segmented, `offline` = dependency-free blank-line split) and
  expands to its **pipeline stages** — the series of tasks it ran: fetch →
  extract → [ocr] → segment → [embed] → commit, each tagged with the engine that
  handled it (text-layer, pandoc, tesseract, vision, llm, offline…), so a failure
  shows exactly which stage broke.
- **OCR review** — pick a document to see its pages: the saved page image beside
  the indexed text, an engine badge per page (**text** = born-digital/plain,
  **vision** = the VLM OCR'd it), and a **Re-OCR (cascade)** button that reruns
  the cheap→gate→VLM cascade on that page's image to show the raw transcription
  and which engine handled it.

Ingest records per-page provenance (engine + page image) under `<home>/pages/`;
documents indexed before this feature show no OCR pages until re-ingested.
Control-plane routes live under `/api/*`. localhost, no auth — don't expose it.

## For agents (agentkit)

raglit's `search` output is exactly the shape agentkit's `ragnotify.ParseHits`
consumes, so one `raglit serve` drives both a model's explicit searches **and**
agentkit's proactive "live-watch" pings (the finder scopes which indexes it
watches via `Opts.ExtraArgs {"index": ...}` — all by default). raglit's
`agent.DocFinder` also plugs straight into a Session's `FinderPreparer`.

## How it's built

- **SQLite FTS5** gives BM25 + the document:page:fragment index in one pure-Go
  dependency (`modernc.org/sqlite`, no CGo). Vectors are stored as BLOBs, cosine
  brute-forced (fine for a local corpus).
- **LLM segmentation** (via [agentkit](https://github.com/iodesystems/agentkit))
  turns pages/text into coherent fragments with a schema-validated fix-loop and
  a safe fallback; an open fragment is carried across page/window boundaries and
  embedded only once it's resolved.
- **Multi-index** — one home holds several named indexes; `serve` searches all
  (RRF-merged, tagged) or a scoped subset.

## Home layout

```
./.raglit/                 (per-project; or $RAGLIT_HOME / ~/local/raglit / --home)
  config.json              endpoint + model settings (from `raglit init`)
  index.sqlite             the default index (index-<name>.sqlite for others)
  originals/               copies of ingested sources
  pages/                   page images for OCR
```

Everything for an index is under one directory — copy it, back it up, or delete
it wholesale.

## Roadmap

- ◻ Daemon auth + remote file upload (today: localhost, shared-FS or URL targets).
- ◻ Vector reranking; opt-in summaries for oversized fragments.
