# MCP contract

What a client of raglit's MCP tools may rely on. `TestContract` in
`cmd/raglit/mcphttp_test.go` holds raglit to it. dun and para read these fields;
changing one is a change to them. Anything not listed may change.

Served the same over stdio (`raglit mcp`) and HTTP (`/mcp/<project>`), except the
tools marked below. Every tool returns one text block of JSON.

## Finding

`search {query, index?, path?, mode?, limit?, siblings?}` returns `{hits: [...]}`,
best first, one row per fragment. A document can appear more than once, so a
client that lists documents dedupes by `doc_id`.

| field | always | meaning |
|---|---|---|
| `doc_id` | yes | the document's root-relative path; pass it to `get_document` |
| `index` | yes | the index, in this client's names |
| `title` | yes | |
| `snippet` | yes | the fragment |
| `rel` | yes | path relative to the corpus root, for showing and mapping |
| `page` | yes | 0 when the document has no pages |
| `similarity` | no | raw cosine, when the vector side ranked the hit |
| `origin` | no | set when the text is not the document's body: `identity`, `fields`, `frontmatter` |
| `summary` | no | the document's summary, when one was generated |
| `caveats` | no | list; each entry is a caveat that applies to one or more hits |

The MCP surface is economical: the path appears once per hit (root-relative),
the caveat text appears once per response, and the internal fields
(`score`, `matched_terms`, `query_terms`, `trust`, `root`) are absent.
The daemon's HTTP `/search` keeps the full shape for the web UI.

With no hits the reply also carries `covers`, what each index holds, so a client
can tell a topic is absent.

`list_indexes {}` returns `{indexes: [{name, meaning, ...}]}`. `meaning` says the
index can search by meaning. When it is false, search is by exact words.

## Reading

`get_document {path, page?, from?, to?, max_chars?, index?}` returns
`{index, path, title, pages, text, truncated}`.

`list_documents {name?, index?}` returns `{documents: [{index, path, title, content_hash, ...}]}`.
`content_hash` is the sha256 of the source bytes.

## Changing an index

`ingest`, `forget`, `manage_index` and `index_status` keep their tool descriptions as
their contract. `ingest` by path needs the admin key, or a project key under a root
it was granted.

`upload` and `manage_project` are served on `/mcp/<project>`, and on stdio only with
`raglit mcp --manage`. They are described in the tools themselves.

## Compatibility

Fields are added, not removed or renamed. A client ignores fields it does not know.
