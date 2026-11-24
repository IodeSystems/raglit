# Crawler-side origin-URL convention

Crawlers live outside raglit. Each one writes files into a directory that raglit
watches (`raglit watch DIR --index NAME`). This note records the convention a
crawler follows so that a search hit can cite the page it came from, not just
the local file path.

## The convention

Write the page URL as the **first line** of the file, on its own line, before
the body:

```
https://example.org/2026/01/some-page
---
The page body starts here.
```

The URL line is ordinary document content. It is indexed and searchable like
any other line, and it is the line a search hit's `snippet` field will show
when the query matches the URL or its path components.

No raglit change is required: the first line is already surfaced on the
operator side by the existing document surface (below).

## Crawler hygiene

Two rules make the convention reliable under raglit's poll-based watcher
(default interval 5 s, `watcher.go`):

1. **Atomic write.** Write to a temporary file in the same directory, then
   `rename` it into the watched directory. A partially-written file can be
   picked up mid-write by the poll and indexed as garbage; the queue's
   content-hash dedup would then key on the incomplete bytes, and a later
   re-crawl of the full file would look like a new document.

2. **Stable file name per URL.** The same URL maps to the same file name.
   The watcher's reconcile step keys by file path and mtime: when the same
   path is modified, the document is re-ingested in place (the same
   `doc_id`), so a re-crawl of a changed page updates the existing document
   rather than adding a new one. A new file name for the same URL would add
   a second document and fragment the index.

## The raglit surface that makes this work

The first-line URL is reachable by every operator-facing document surface
that already exists:

- **Search hit `snippet`.** Each hit in `/search` (and the MCP `search` tool)
  carries a `snippet` — the fragment of indexed text around the match. A
  query that hits the URL line surfaces it directly.
- **`get_document`** (MCP tool) / **`GET /api/get-document?path=…`**. Returns
  the full indexed text of a document. The first line is the URL.
- **Facets** (`/search` response `facets` field). Per-facet-value counts over
  the full matching set. If the crawler tags files (e.g. via a filename
  convention) the facet surface groups them without a separate store.
- **Analyze** (MCP `ingest` with `analyze: true`). Post-hoc dump of every
  stored fragment for a document, including the first line.

## Deferred: manifest / sidecar

A manifest file in the watched directory mapping file name → URL, surfaced
as a document source by raglit, is **deferred**. It would give a structured
origin that is not dependent on the first line of the body, but it is only
built if the interim (first-line URL) is tried on a real corpus and shown
insufficient — for example, if a crawler's body format makes the first line
ambiguous, or if an operator needs to filter by origin without matching a
URL substring.

The reopen condition: an operator reports that the first-line convention does
not give them the origin information they need in a way the existing surface
(snippet, `get_document`, facets) does not already provide.

## See also

- `docs/design/icebox.md` — the standing decision that crawlers live outside
  raglit (USER 2026-10-23).
- `cmd/raglit/watcher.go` — poll interval and reconcile logic.
- `cmd/raglit/httpd.go` — `/api/get-document`, `/search` facets.
