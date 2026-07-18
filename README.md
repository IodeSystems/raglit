# raglit

A local, composable document RAG index. One portable **SQLite** file is the
whole index; **FTS5** gives BM25 lexical ranking built in, so "BM25" and the
`document:page:fragment` store collapse into a single **pure-Go** dependency
(`modernc.org/sqlite`, no CGo → single static binary).

Built on [agentkit](../agentkit): raglit's search implements `agent.DocFinder`,
so a local index drops straight into agentkit's proactive-retrieval seam
(`agent.FinderPreparer`) and MCP tool bridge — the same interface a remote
service satisfies. Start local, scale out by swapping the impl, no rewrite.

## Setup

raglit is unusable until it knows an endpoint. Run the wizard (also what a
no-arg `raglit` launches when unconfigured):

```
raglit init
```

It asks for an OpenAI-compatible base URL + token, queries `/v1/models`, and
lets you pick a **vision model** (image in → text, for PDF OCR) and an
**embedding model** (text in → vector). Written to `<home>/config.json`;
`--llm-*` flags override it per command.

Try it with zero setup:

```
raglit demo
```

## Use

```
raglit index  [--home DIR] [--embed] FILE|DIR...   ingest local files (+ PDFs)
raglit ingest [--home DIR] [--now] URL...   queue URL(s): file://<path>, http(s)://...
raglit work   [--home DIR] [--embed]        drain the ingest queue once
raglit status [--home DIR]                  index + queue status
raglit search [--home DIR] [--mode M] [-n N] "query"   M = bm25 | vec | hybrid
raglit serve  [--home DIR] [-n N] [--embed]   stdio MCP server
```

## Lazy ingest + status

`ingest` (and the MCP `ingest` tool) is **lazy**: it queues a URL and returns a
job id immediately. A worker — the background of `serve`, or a one-shot
`raglit work` — fetches and indexes each job (`file://` local, `http(s)://`
remote; PDFs OCR'd when a vision model is configured). Because it's queued, you
can ask for progress:

```
$ raglit status
index: 12 document(s), 48 fragment(s)
jobs:  done=3 running=1 pending=8 failed=0  (42.0/min)
  running  #4 http://…/spec.pdf   (eta ~1s)
  pending  #5 file:///docs/a.md   (eta ~3s)
  …
```

## MCP tools (`raglit serve`)

One server hosts a SET of named indexes (`index.sqlite` = `default`,
`index-<name>.sqlite` for the rest, all under one home).

- `search` — ranked fragments. `index` selects one or a comma-separated set;
  omit it to search **all** (RRF-merged, each hit tagged with its `index`).
- `ingest` — queue a URL for lazy ingestion into an `index` (default `default`,
  created on demand).
- `index_status` — counts / rate / ETAs for an `index`; omit to aggregate all.
- `list_indexes` — the indexes with their document/fragment counts.

`search` output matches agentkit's `ragnotify.ParseHits`, so one server drives
both the model's explicit searches and agentkit's proactive (live-watch) pings.
**Index selection for the live watch:** the finder searches all indexes by
default; scope it by setting `ragnotify.MCPFinder` `Opts.ExtraArgs` to
`{"index": "a,b"}`.

## Fragment sizing

Fragments target ~500 words (a coherent subsection). The segmenter binds small
related units together to reach the floor — below it, a hit lacks the context to
concept-chain; a soft ceiling stops one fragment swallowing a document.
Oversized matches are surfaced to an agent as pointer notifications (fetch on
demand), so no summarization pass is needed.

## Home layout

Everything for one index lives under a single home directory, so it's a portable
unit you can copy, back up, or delete wholesale. Default `$RAGLIT_HOME`, else
`~/local/raglit`; override with `--home`, or point at a raw index file with
`--db` (skips originals storage).

```
<home>/
  index.sqlite   the FTS5 index (documents:pages:fragments)
  originals/      copies of ingested sources — the index stays self-contained
  pages/          page images for OCR (populated by the PDF pipeline)
```

```go
s, _ := raglit.Open("idx.sqlite")
s.Ingest(raglit.Document{Path: "auth.md", Title: "Auth", Fragments: []raglit.Fragment{
    {Page: 1, Ord: 0, Text: "Access tokens expire; the refresh token rotates."},
}})
hits, _ := s.Search("token refresh", 10)          // BM25, best first
finder := raglit.NewFinder(s)                       // → agent.DocFinder
```

## Grain

`documents → fragments(page, ord, text)`. A fragment is one indexable unit; page
+ ord locate it back in the source, so a hit is a precise citation.

## Roadmap

- ✅ **Lexical core** — FTS5 BM25 index/search + `agent.DocFinder`.
- ✅ **MCP `serve`** — search exposed as an MCP tool; output feeds both the
  explicit channel and agentkit's proactive-notify (`ragnotify`).
- ✅ **PDF → OCR** — `pagify` (pure-Go pdfcpu, image/scanned PDFs) + `ocr`
  (vision-LLM via agentkit's multimodal `llm` client) → feeds the same index.
  `index` handles PDFs end-to-end; page images persist under `pages/`.
- ✅ **Vectors (opt-in)** — `index --embed` embeds fragments via nomic
  (stored as sqlite BLOBs); `search --mode vec` (brute-force cosine) and
  `--mode hybrid` (BM25 + cosine, reciprocal-rank fusion). Pure-Go, no vector
  extension. A custom NSW sidecar is the escalation only if a linear scan gets
  slow — measured, not assumed.
