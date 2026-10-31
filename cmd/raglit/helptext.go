package main

// Written once from the old usage() text and edited by hand since; not
// generated code. Root help is short (USER 2026-10-23: "the raglit --help is
// so damn busy, it's impossible to discover this"); the detail lives here and
// prints on `raglit help <command>`.

type helpEntry struct {
	name, short, detail string
}

type helpGroup struct {
	name, title string
	entries     []helpEntry
}

var helpGroups = []helpGroup{
	{"", "common", []helpEntry{
		{"watch", "DIR --index NAME — index a directory and keep it fresh", `raglit watch DIR --index NAME
              index DIR into NAME and keep it fresh. Creates the index if it is
              missing. Adds DIR to indexes.NAME.roots in this project's config,
              sets "watch": true, queues the files, and registers the project
              with the daemon, which re-ingests on change. Safe to run again.
raglit watch [start|list|stop]
              register this project's configured roots with the daemon, list
              every watched project, or stop watching this one.`},
		{"search", "\"query\" — search the indexes that apply here", `raglit search [--index A,B] [--mode M] [-n N] "query"
              no --index: the indexes whose scopes match this directory, plus
              the ambient global ones. --index names exactly what to search
              ("proj:idx" for another project's). --project P with no --index
              searches P's indexes. Needs no project.
              --mode: hybrid (default where the index has embeddings, else
              bm25) | bm25 (exact words) | vec (meaning). Each hit shows
              "sim" (cosine) and "N/M terms" (query words it contains).`},
		{"status", "indexes and queue progress", `raglit status [--home DIR]                 index + queue status (done/pending/rate/eta)`},
		{"index", "--list | NAME CMD — list indexes, or work on one", `raglit index --list [--json]
              every index: documents, state, scopes, and ✓ where a search from
              here with no --index reads it. --json: the daemon's fields per
              index plus project, index and selector ("proj:idx", or ":idx"
              for a global index), for a tool to pick from.
raglit index NAME scope [add|rm PATTERN...]
              the directories an index applies in. A directory covers itself
              and everything under it; a glob (*, **) matches directories.
              "." is this directory. An ingest from a project gives a new
              index that project's directory.
raglit index NAME global [ambient|opt-in]
              no scopes: every search reads it (ambient), or only a search
              that names it (opt-in, the default).
raglit index NAME status|search|forget|pause|resume|purge|prune|unwatch ...
              one index: queue state, search it, drop paths from it, pause or
              resume its work, purge a glob, or stop watching a directory.
              raglit index NAME -h lists the subcommands. NAME may be
              proj:idx.
raglit index [--home DIR] FILE|DIR...
              queue local text and PDF files on the daemon and wait for it.`},
		{"mcp", "stdio MCP server for an LLM session", `raglit mcp  [--home DIR] [-n N]   stdio MCP server (search + ingest + index_status)
              "raglit serve" is the old name and still works.`},
		{"daemon", "the shared daemon (REST, web UI, MCP backend)", `raglit daemon [--root DIR|--home DIR] [--addr :7420] [--embed]   multi-protocol daemon:
              REST + review UI at / + OpenAPI (/openapi.json) + GraphQL (/graphql); scoped
              per-index storage under --root (default ~/.raglit); --home for a single index;
              records <root>/daemon.json for client discovery; --stop signals a running one,
              --restart replaces it with a detached one on these flags (after a rebuild)`},
		{"service", "run the daemon under systemd or launchd", `raglit service install|uninstall|start|stop|restart|reload|status|logs|login-admin
              run the daemon under systemd (Linux) or launchd (macOS). reload
              re-reads config; the daemon re-execs once no job is running.`},
		{"init", "set up this project (endpoint, models)", `raglit init   [--home DIR]                 configure endpoint + models (writes ./.raglit)`},
		{"doctor", "check tools and model endpoints", `raglit doctor [--home DIR] [--fix-unit] [--json]   OCR readiness: cheap engine + vision endpoint;
                                             a stale systemd unit (--fix-unit rewrites it);
                                             what raglit can do here and what would add more
              --json  {capabilities, roles, extractors, update, ...} for tools
                      such as dun; update is poly-lsp's shape (24 h cache)`},
	}},
	{"ingest", "queueing, re-reading and removing documents", []helpEntry{
		{"ingest", "queue folders, files or URLs", `raglit ingest [--home DIR] [--wait] TARGET...  queue folders/files/URLs (file://, http(s)://)
              --wait follows the daemon until the index's queue is empty.
              --fresh re-reads a document even if nothing changed: skips the
              unchanged-bytes check AND the cross-index pool. Use it when a cached
              result is wrong for a reason the cache key cannot see.`},
		{"sync", "ingest the config's index roots", `raglit sync   [--home DIR] [--index N] [--dry-run]   ingest the config's [indexes] roots
              (per-index include/ignore globs, honors .gitignore; dedup skips unchanged)`},
		{"forget", "drop documents from the index; files stay", `raglit forget [--dry-run] <path|dir>...
              drop a document from the INDEX, with its page images. A file on
              your disk is untouched; an uploaded file is deleted with it.
              Not a withdrawal: no grounds are recorded and nothing stops a
              re-index — for a row that should never have existed (raglit's own
              transcription sidecars, indexed as documents) rather than for a
              document somebody ruled out.`},
		{"purge", "remove documents matching a glob", `raglit purge [--yes] [--daemon URL] INDEX PATTERN
              remove every document whose path matches PATTERN (a glob:
              **/node_modules/** for a directory anywhere), with its pending
              jobs and page images. Reports what would go unless --yes. Files
              on your disk stay; uploaded files go with their documents.`},
		{"prune", "remove documents whose file is gone", `raglit prune [--index INDEX] [--yes] [--delete-orphan-originals] [--daemon URL]
              documents whose local file was deleted (a removed worktree,
              a deleted directory) are out of search already; prune lists
              them per index and per deleted directory, and removes them
              with --yes. No --index: every index. status and index list
              show the count. A URL, an upload or a library-ingested name
              is never prunable.
              Also reports page images no document names (--yes removes
              them) and dangling copies under originals/, which raglit no
              longer writes or reads (--delete-orphan-originals removes
              them; an upload's copy stays).`},
		{"pause", "stop an index's work; jobs keep queueing", `raglit pause|resume [--daemon URL] [INDEX...]
              a paused index runs no work: no ingest, captions or embeds. Jobs
              still queue and run on resume. No INDEX lists every index.`},
		{"resume", "resume a paused index", `raglit pause|resume [--daemon URL] [INDEX...]
              a paused index runs no work: no ingest, captions or embeds. Jobs
              still queue and run on resume. No INDEX lists every index.`},
		{"retry", "requeue failed ingest jobs", `raglit retry  [--home DIR] [--dry-run] [--match S]   requeue failed ingest jobs`},
		{"reread", "drop cached page OCR and read again", `raglit reread [--suspect] [--dry-run] <path>...      purge cached page OCR and read again
                                                     (a re-index alone CANNOT fix a bad read —
                                                      the page cache returns the same answer)
              (skips jobs whose file is gone — a rename outlives the row)`},
		{"work", "wait until the daemon has worked the queue", `raglit work   [--home DIR] [--index N]     wait until the daemon has worked the index's queue, then print status`},
		{"refragment", "re-ingest documents with oversized fragments", `raglit refragment [--dry-run]              re-ingest documents whose fragments are
              larger than the embed model accepts (probes the limit, stores it)`},
		{"branch", "copy-on-write branch indexes", `raglit branch list|fork NAME|delete NAME   manage copy-on-write branch indexes
              (daemon only; namespaced + scoped to this project)`},
		{"drop-index", "delete one index's storage", `raglit drop-index --index NAME
              delete one index's storage. The shared document pool stays, so the
              same files indexed again reuse the cached reads.`},
		{"maintain", "fold a shared project's public layers into its base", `raglit maintain [--dry-run] PROJECT        fold a shared project's public layers into its
              base (run as the base's owner, e.g. a maintainer machine user)`},
	}},
	{"documents", "what documents are: captions, tags, schemas, fields", []helpEntry{
		{"identify", "caption, summary, kind and tags per document", `raglit identify [--list] [--force] [--tags-only] [--limit N] [--wait] [--dry-run] [DOC...]
              what a document IS, as opposed to what its file is called: a
              caption, a summary and a kind, asked of the model on the text
              already indexed. Ingest does this per document; this is for a
              corpus indexed before it existed. With no arguments it QUEUES
              every document that has no name yet and returns — the rows are
              durable, the daemon works them two at a time (the endpoint's
              concurrency), and killing the terminal loses nothing. --wait
              follows the queue; 'raglit status' shows what is outstanding.
              The file is NEVER renamed: the caption is a display name and a
              search target, and the summary is indexed so a query for
              "purchase and sale agreement" can rank a document whose body
              never says it. Search marks such a hit — findable by it, not
              quotable from it.

raglit identify --name "..." [--summary "..."] [--kind K] <DOC>
              a PERSON saying what the document is. Supersedes the machine's
              caption and is never regenerated over.

raglit identify --tags-only [--force] [DOC...]
              the TAG backfill: what each document is about, and what job it
              does in the corpus, asked for documents that already have a
              caption. Written to the tag columns alone — the caption, its
              author and the text it was written from are left exactly as
              they are, which is what makes this safe to run over a corpus
              somebody has already corrected by hand.`},
		{"fields", "extract or set a typed document's fields", `raglit fields [--list] [--force] [--limit N] [--dry-run] [DOC...]

raglit fields --set '{"...":"..."}' <DOC>
              fill out the schema of documents that resolved as a type, or
              read one back. Queued and drained like identify — same rows,
              different ask. The extraction is indexed, so a query for a work
              order number ranks the document carrying it, and it is MARKED as
              generated: it is a model's reading of a form, not the form.
              --set records a person's, which is never regenerated over.`},
		{"doctype", "the schemaed document types of this index", `raglit doctype list | show <NAME> | rm <NAME>

raglit doctype propose [--save] <NAME> <DOC>...

raglit doctype add --file <F> <NAME>
              the SCHEMAED documents of this corpus. Some documents are forms
              — receipts, work orders, lab reports, bills — and a hundred of
              those are worth far more as a hundred records than as a hundred
              summaries. Which fields, and what they are called, is a property
              of the corpus and not of raglit, so the vocabulary is per-index
              and authored: 'propose' reads GOLD documents you picked and
              proposes the schema and the reading instructions, printing them
              for review rather than registering them. Identity then resolves
              each document as one type or none.`},
		{"hint", "tell the models this corpus's conventions", `raglit hint [--set "..." | --clear]
              what YOU tell the models about this corpus: its conventions, its
              abbreviations, which of its ambiguities resolve which way. A
              model reading one page is answering a general question about a
              specific collection, and the collection is the half it cannot
              see — "RO" on a garage's paperwork is a repair order, not
              "received", and nothing on the page says so. The hint reaches
              the page transcription, the segmentation, the identity and every
              extraction, and it is part of the READING RECIPE: change it and
              documents read under the old one keep that reading until they
              are ingested again.`},
		{"about", "what this index holds, counted", `raglit about [--write] [--json]
              what this INDEX is: how many documents, which kinds, which tags
              — counted, so never stale — and, with --write, a paragraph the
              model writes from the captions. Search returns the counted half
              automatically when a query finds nothing, so an agent can tell
              "not in this corpus" from "badly phrased".`},
		{"audit-tags", "tag vocabulary drift; --merge", `raglit audit-tags [--min-count N] [--documents] [--json]

raglit audit-tags --merge "old,other=>new"
              the tag vocabulary and the drift in it. Content tags are an open
              vocabulary, so "lead paint", "LBP" and "paint inspection" arrive
              from three documents meaning one thing; the identity prompt is
              seeded with the index's established tags to stop most of that as
              it happens, and this reports what got through. ≈ marks tags
              sharing a word — a PROPOSAL. Whether two terms mean the same
              thing is not something spelling establishes, so --merge applies
              only what a person named.`},
	}},
	{"provenance", "duplicates, versions, readings, withdrawals, slices", []helpEntry{
		{"similar", "near-duplicates and containment, at page grain", `raglit similar [--build|--rebuild|--status|--all] [--json] FILE|DOCPATH...
              near-duplicate + containment detection over shingles, at PAGE grain.
              Reports both directions (a deed INSIDE a title commitment has
              containment 1.0 one way and a Jaccard of 0.05), the page alignment
              ("probe p12-14 = match p1-3"), and which NUMBERS differ between two
              copies of one instrument. Local computation — no model, works offline.
              --build sketches documents that have none; --all audits the corpus.`},
		{"diff", "compare two transcripts page by page", `raglit diff [--json] A B
              compare two transcripts PAGE BY PAGE: byte identity, a per-page
              match rate, which pages are missing, and the numbers that differ.
              A whole-document 0.93 is equally consistent with a clean re-scan
              and with a refiling whose page 3 changed; the per-page shape is
              what tells those apart, and an average destroys it.`},
		{"mark", "rule a pair: copy, version or unrelated", `raglit mark <A> <B> <copy|version|unrelated> [--supersedes PATH] [--note ...]
              record that ruling. Appended to raglit-audit.jsonl beside the
              documents and projected into judgements.db, never into .raglit/ —
              a ruling cannot be recomputed, so it must outlive a reindex and
              reach the other machines. The trail is the record: marks
              --rebuild reconstructs the database from it.`},
		{"marks", "rulings on overlapping pairs; --todo, --identical", `raglit marks [--todo] [--json] [DOCPATH]
              what an overlapping pair IS, as opposed to how much text it shares.
              --todo lists pairs nobody has ruled on, each with a proposal: a
              COPY is the same instrument again (a scan of a PDF already held);
              a VERSION is the same instrument filed or corrected differently,
              and both matter. No score separates them — a re-recorded deed and
              a scan of one both align at 0.97 — so raglit proposes from WHAT
              disagrees and a person rules.

raglit marks --identical [--write] [--json]
              documents held more than once BYTE for byte. Needs no sketches —
              the index already knows the hashes. This is the one relation with
              no judgement in it, so --write records them as copies attributed
              to raglit rather than to you; a person's ruling still overrides.`},
		{"readings", "every recorded reading of a page", `raglit readings <DOC> [PAGE] [--text] [--json]
              every recorded version of what a page says, oldest first, with
              the reading in force marked. A correction attests that a better
              reading exists; it does not erase the old one, because a
              quotation taken before it matched the text in force then.`},
		{"withdraw", "rule a document out of the corpus, with grounds", `raglit withdraw --reason "..." [--by WHO] [--dry-run] <path|dir>...
              rule a document OUT of the corpus, with grounds. Not a delete:
              the file stays on disk, the reason is recorded in the audit
              trail, and the INGEST path honours it — so the next sweep does
              not quietly put it back. Names what still cites the document,
              and rewrites nothing: a citation is a claim its author made.
              A directory withdraws what is indexed beneath it.`},
		{"withdrawn", "what has been ruled out, and why", `raglit withdrawn [--json]     what has been ruled out of the corpus, and why`},
		{"slice", "declare a page range of a bundle as a document", `raglit slice <DOC> <FROM-TO> [--title T] [--id ID] [--no-materialize]
              declare that a page range of a bundle is a document, and build a
              citable child from it. The bundle is never cut up — it is what
              was filed — and the child keeps the PARENT's page numbers, so a
              quotation from it stays checkable against the exhibit as filed.`},
		{"slices", "declared sub-documents and coverage", `raglit slices [DOC] [--materialize] [--json]
              declared sub-documents, with coverage: which pages of a bundle
              no slice claims. That is what says a bundle is fully linearized
              rather than merely started.`},
		{"import-readings", "adopt verified transcripts as attested readings", `raglit import-readings [--dry-run]
              adopt verified transcripts already in the corpus as attested
              readings of the recordings they transcribe. Dry run by default.`},
	}},
	{"pages", "page images, regions, OCR, transcripts", []helpEntry{
		{"pagify", "extract page images from a PDF", `raglit pagify [--out DIR] FILE.pdf...      extract page images (image/scanned PDFs)`},
		{"regions", "read a page as a tree of regions", `raglit regions [--page N] [--depth D] [--write] FILE   read a page as a TREE of
              regions — for a sheet whose text is too small to survive one look.
              Asks the model what is here and where to look closer, and descends.
              --write records the tree in <doc>.raglit-regions.json beside it`},
		{"region", "re-render the exact crop a passage was read from", `raglit region [--list|--locate TEXT] FILE [REGION-ID]   re-render the exact crop
              a passage was read from: same bbox, same rotation, same dpi, and a
              digest check that it IS that image. PNG to stdout, or --out FILE.
              --locate says which region a quotation came from`},
		{"attest", "a person confirms or corrects region reads", `raglit attest [--port N] FILE              hand a recorded region read to a person:
              serves a review where each region can be confirmed, corrected or
              marked unreadable, beside the exact crop its text was read from.
              Verdicts append to <doc>.attest.jsonl; the region read is untouched`},
		{"ocr", "transcribe page images with the vision model", `raglit ocr    [--llm-*] IMAGE...           transcribe page images via a vision model`},
		{"transcribe", "page-delineated markdown of a document", `raglit transcribe [--write] FILE...        page-delineated markdown of a document
              (--write puts <doc>.raglit-transcription.md beside it; index option
               "writeback_transcription_md" does the same during ingest)`},
		{"migrate-attachments", "move extracted mail attachments into raglit storage", `raglit migrate-attachments [--dry-run]
              move extracted mail attachments out of the corpus into raglit's own
              storage. Dry run by default.`},
	}},
	{"admin", "review UI, corpus health, demo", []helpEntry{
		{"review", "the daemon with the review-UI banner", `raglit review [--root DIR|--home DIR] [--addr :7420] [--embed]   same daemon, review-UI banner`},
		{"health", "what is wrong with this corpus, worst first", `raglit health [--json] [--kind K] [--quiet]
              what is WRONG with this corpus, worst first: documents that are
              indexed and unsearchable, PDFs with no pages, failed jobs with
              the stage they died in, pages the model would not segment, and
              jobs the endpoint made fight for. Exits non-zero if anything is
              wrong, so it works as a check and not only as a page to read.
              (A withdrawal is a decision, not a fault, and never fails it.)`},
		{"demo", "self-contained offline tour", `raglit demo                                self-contained offline tour`},
	}},
}
