# raglit

A local document RAG index you can stand up in one command. Point it at a
folder, ask questions, or hand it to an agent as an MCP tool. One portable
SQLite file is the whole index — pure-Go, single static binary, no services to
run.

## Quickstart

```sh
# build
go install github.com/iodesystems/raglit/cmd/raglit@latest

# 1. one-time setup — pick an OpenAI-compatible endpoint + models (a wizard)
raglit init

# 2. ingest a folder (code, markdown, text; PDFs get OCR'd)
raglit ingest ./my-project --now

# 3. ask
raglit search "how does the auth token refresh work?"
```

That's it. `init` asks for a base URL + API key and lists the endpoint's models
so you can pick a chat/vision model and an embedding model; everything else uses
sensible defaults (you never pass model flags again). The index lives in
`~/local/raglit` by default.

> No endpoint handy? Every offline piece works without one:
> `raglit demo` runs a self-contained tour, and text ingest falls back to a
> dependency-free splitter when no model is configured.

## What it does

- **Ingest** folders, files, or URLs (`file://`, `http(s)://`) — lazily (queued)
  or with `--now`. Text/code is segmented by the model into coherent, ~500-word
  fragments (functions bound with their docs, not atomised); PDFs are OCR'd page
  by page with cross-page stitching.
- **Search** — BM25 (`--mode bm25`, default), vectors (`--mode vec`), or hybrid
  RRF (`--mode hybrid`). Results are precise citations: document → page →
  fragment.
- **Serve** — expose the index(es) to any MCP client (Claude Desktop, agentkit):

  ```sh
  raglit serve
  ```

  Tools: `search`, `ingest`, `index_status`, `list_indexes`.

## Commands

```
raglit init                          configure endpoint + models (wizard)
raglit ingest TARGET... [--now]      queue folders / files / URLs (lazy; --now drains)
raglit search "query" [--mode M]     M = bm25 | vec | hybrid
raglit status                        documents/fragments, queue progress, rate, ETAs
raglit serve                         stdio MCP server
raglit demo                          offline, self-contained tour
```

`--home DIR` picks the index home; `--index NAME` selects a named index within
it (default `default`).

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
~/local/raglit/            (or $RAGLIT_HOME, or --home)
  config.json              endpoint + model settings (from `raglit init`)
  index.sqlite             the default index (index-<name>.sqlite for others)
  originals/               copies of ingested sources
  pages/                   page images for OCR
```

Everything for an index is under one directory — copy it, back it up, or delete
it wholesale.

## Roadmap

- ◻ Daemon mode — a long-running `serve` that other invocations call into
  (remote ingest/search), so large ingests run in the background and CLIs don't
  contend on the index file.
- ◻ Vector reranking; opt-in summaries for oversized fragments.
