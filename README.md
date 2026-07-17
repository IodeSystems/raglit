# raglit

A local, composable document RAG index. One portable **SQLite** file is the
whole index; **FTS5** gives BM25 lexical ranking built in, so "BM25" and the
`document:page:fragment` store collapse into a single **pure-Go** dependency
(`modernc.org/sqlite`, no CGo → single static binary).

Built on [agentkit](../agentkit): raglit's search implements `agent.DocFinder`,
so a local index drops straight into agentkit's proactive-retrieval seam
(`agent.FinderPreparer`) and MCP tool bridge — the same interface a remote
service satisfies. Start local, scale out by swapping the impl, no rewrite.

## Use

```
raglit index  [--home DIR] FILE|DIR...   ingest text/markdown
raglit search [--home DIR] [-n N] "query"   BM25-ranked fragments
raglit serve  [--home DIR] [-n N]           stdio MCP server (search tool)
```

`serve` exposes one `search` tool returning `{hits:[{doc_id,title,page,score,
snippet}]}` — the shape agentkit's `ragnotify.ParseHits` consumes, so one server
drives BOTH the model's explicit searches and agentkit's proactive pings.

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
- ◻ **Vectors (opt-in)** — sqlite-vec or a custom NSW sidecar, only if BM25
  lexical recall proves insufficient. Measured, not assumed.
