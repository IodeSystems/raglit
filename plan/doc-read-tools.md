# raglit: MCP document-read tools (list_documents + get_document)

Status: done 2026-07-23.

## Goal (from user)

An agent needs a document's text over MCP. Existing tools couldn't: `search`
returns 300-char snippets, `list_indexes` lists indexes not docs, `ocr`
re-extracts a file the caller supplies (not the index). No way to enumerate
documents by filename or pull a doc's full indexed text.

## Delivered — two MCP tools (`serve.go`)

- **list_documents** `{name?, index?}` → `{documents:[{index,path,title,
  fragments,pages,vision}]}`. `name` = case-insensitive substring over path+title;
  `index` empty = across all indexes (each doc tagged with its index). Wraps
  `Store.Documents()`.
- **get_document** `{path, page?, from?, to?, max_chars?, index?}` →
  `{index,path,title,pages:[{page,text}],text,truncated}`. `path` = exact stored
  path OR a unique filename substring (ambiguous → error listing candidates; none
  → error). Resolves across all indexes unless `index` set. Text reassembled from
  fragments in page/ord order; `page` or `from`/`to` bound an inclusive range;
  `max_chars` caps the joined `text` blob (per-page array stays whole).

Store side (`docget.go`): `MatchDocuments(ref)` (exact-then-substring resolver,
returns []DocRef) and `DocText(exactPath, from, to, maxChars) DocContent`.

## Verified

- Unit (`docget_test.go`): MatchDocuments exact/substring/title/broad/none/empty;
  DocText full + page-range + max_chars cap (pages kept whole) + page-0 text doc
  + unknown-path error. Green.
- Live MCP stdio: both tools advertised; `list_documents{name:"auth"}` found the
  doc; `get_document{path:"auth",max_chars:220}` returned page-0 text; a broad
  substring returned the ambiguity error with candidates.

## Notes / not done

- These are MCP (`serve`) tools; the daemon HTTP already has `/api/documents` +
  `/api/doc` (the latter OCR-pages only) — not unified with these.
- No content search-within-document / offset paging beyond page ranges + max_chars.
- get_document resolves across all indexes by default; a substring unique within
  one index but colliding across indexes is reported ambiguous (pass `index`).
