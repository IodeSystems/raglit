# raglit: document identity — what a document IS, in its own words

Status: BUILT 2026-08-01 (`identity.go`, `identityqueue.go`, `raglit identify`,
`/api/identify`, `/api/identify/queue`).
Living doc — prune as the follow-ups land.

## Goal (from user)

> "it's clear that these documents need a better name than the ones that we have
> given them, part of the transcript analysis should include a summary of what
> the document is, what it covers and a better name … this summary and name could
> also be indexed"

## Why — the filenames are wrong, and one of them lied

A corpus is named by whatever produced its files: a scanner, a mail client, a
download. Measured on the ardley index, 406 documents:

| Name as stored | What it is |
|---|---|
| `24053 North Northlea - Form 22J -Lead-Based Paint (1).pdf` | the **complete 30-page Form 21 purchase and sale agreement** — the buyer's signed offer |
| `0428_001.pdf` | the 28-page **executed** counterpart of that agreement |
| `1636_001.pdf` | a county access permit plus a 1993 escrow letter |
| `2021-form-21-PSA-second-authentisign-envelope-F2AA1FA9.pdf` | correct, but only because a person renamed it by hand after reading it |

The first is the case that matters. The filename did not merely fail to describe
the document — **it named a different document**, and the agreement at the centre
of a live dispute sat unread behind it. It was found by a person opening files one
by one. Nothing in the index could have surfaced it: the title was the filename,
the search text was the body, and no query for "purchase and sale agreement"
ranks a file called "Lead-Based Paint".

Scanner names (`0428_001.pdf`, `1636_001.pdf`) are the same problem without the
malice. They carry no signal at all, so a document list of 406 of them is a list
nobody can navigate.

## Delivered

Three fields, asked of the model ONCE per document on the assembled transcript —
the point where the whole text exists in one place and has cost nothing extra to
obtain:

- **`name`** — the caption a person would file it under.
- **`summary`** — the instrument, the parties, the date, the ground it concerns,
  what it does.
- **`kind`** — from a CLOSED vocabulary, settled here rather than by whatever the
  first model returned: `deed · survey · agreement · correspondence ·
  court filing · certification · analysis · other`. `NormalizeKind` maps the
  aliases a model actually emits (letter/email/memo → correspondence, contract/
  lease → agreement, plat → survey, motion/petition/declaration → court filing);
  anything else is REFUSED and re-prompted with the list. "other" is the escape
  hatch and is meant to stay rare — a corpus where it is common means the
  vocabulary is wrong, and the fix is to add a term deliberately, once.

Stored on `documents` (`gen_name`, `gen_summary`, `gen_kind`, `gen_source`,
`gen_model`, `gen_at`) and indexed as ONE fragment marked `fragments.origin =
'identity'`. That is the part that fixes discovery: a query for "purchase and
sale agreement" now ranks a document whose body never says those words in a form
BM25 can rank, because the summary does.

### The rules that keep it a machine claim

- **The file is never renamed.** The path is what fragments, page images, region
  trees, readings, verdicts, the audit trail and every citation already written
  into a legal packet join on. Both names are shown everywhere (CLI, review UI,
  `/api/documents`), because "this file is called X and is actually Y" is itself
  the finding.
- **The generated text is marked, and the mark is enforced in both directions.**
  `fragments.origin` distinguishes it in the database, the fragment's own first
  line says a machine wrote it (text travels — into a search result, a context
  window, a clipboard), search results carry `origin` and say so, and every path
  that REASSEMBLES a document filters it out: `DocText`/`get_document`,
  `TruePages`, `ReferencesTo`, the pool export, and the per-document fragment
  count.
- **A person can overrule it and their caption is never regenerated.**
  `gen_source` is `machine` or `person`; `raglit identify --name …` records a
  person's, and both the ingest path (`identityForIngest`) and the commit itself
  (`commitDoc`) refuse to overwrite it — the second is the one that matters,
  because it is the single point every ingest path passes through, including
  pooled reuse carrying another index's caption.
- **A failure yields no identity, never a guess.** The model output is
  schema-validated with the segmenter's fix-loop; a caption that is empty, a
  summary that distinguishes nothing, or a kind outside the vocabulary is
  re-prompted, and a run that never produces a valid answer returns an error. A
  document with no caption is still findable by filename; a document with a WRONG
  caption is one whose list entry lies with a machine's confidence.
- **A bad identity never fails an ingest.** It is recorded as a `warn` stage row
  (`identity`), so a half-captioned corpus says which half and why instead of
  being silent.

### Where it plugs in

- `identity.go` — `DocIdentity`, the vocabulary, `Identifier` (the model call),
  the store reads/writes, `IdentifyDocument` (re-run) and `RecordIdentity` (a
  person's).
- `pipeline.go` — `identityForIngest` after the transcript is assembled;
  `commitDoc` writes the columns AND re-emits the fragment inside the atomic
  swap, so columns and searchable text can never disagree.
- `pool.go` — `PooledDoc.Identity` carries the caption across indexes (it is a
  model call, which is what the pool exists to avoid paying twice); the identity
  fragment is excluded from the pooled fragments and rebuilt on import, so reuse
  cannot duplicate it or replay it as document text.
- `identityqueue.go` — captioning is QUEUED work (`identity_jobs`), not a loop
  inside a command. Enqueueing is instant and durable; the daemon's identity
  worker drains the rows; a machine that dies mid-sweep resumes where it stopped
  (orphaned `running` rows are requeued, because a caption is one bounded call —
  unlike an ingest job, which may have been killed BY its document).
  Concurrency is the endpoint's, not an index's: `identity_slots` (default 2) is
  what the server actually serves, and past that requests queue INSIDE the server
  where raglit cannot see, resume, or distinguish them from an ingest job's OCR
  call. Indexes are drained one at a time for the same reason. The worker is a
  pipeline — a loader claims rows and reassembles text, the slot-holders do
  nothing but the model call, a committer writes back — so database work never
  occupies a slot. A document with nothing to read closes `skipped` (not failed,
  not re-queued).
- `raglit identify` — the re-runnable half, for a corpus indexed before this
  existed. No arguments queues every document that has none; `--force` redoes a
  machine's; `--wait` follows the queue; `--list` reports coverage;
  `--name/--summary/--kind` records a person's. `raglit status` shows what is
  outstanding. On an embedded index with no daemon, the command drains the same
  queue in-process.
- `POST /api/identify` — the same two operations daemon-side, because a write on
  a daemon-routed project belongs to the daemon (storeroute.go) and because the
  daemon is what holds the model.
- Config — `identity_model` (empty → the vision model, which every home already
  has and which is a chat model), `no_identity` to turn it off.

### Verified

- `identity_test.go`: the caption/summary/kind round trip; the fix loop refusing
  an invented kind; refusal rather than a guess; the vocabulary and its aliases;
  the head+tail excerpt with the gap marked; the summary searchable AND excluded
  from `DocText`/`TruePages`/the fragment count; one identity fragment however
  many times it is written; identity surviving a re-ingest with its fragment;
  a person's caption surviving an ingest carrying a machine's; `IdentifyDocument`
  keeping/forcing/refusing correctly; the pool round trip.
- `cmd/raglit/identifycmd_test.go`: coverage listing names the unnamed; a
  person's ruling sticks and does not rename the file; an unknown kind is
  refused; no model configured says so.
- Live, against the configured endpoint: a text document ingested with `raglit
  index` came back captioned "2021-05-25 Purchase and Sale Agreement
  (Ardley/Brannock)", kind `agreement`; a person's `--name` superseded it; a
  re-ingest and a `--force` sweep both left the person's caption alone; the
  search hit on the summary printed marked.

## Open / not done

- **The summary is NOT embedded**, even on an `--embed` index — lexical indexing
  is unambiguously right, and whether a paraphrase belongs in the same vector
  space as the document's own words is the open question. Decide by measuring on
  the ardley corpus, not by argument.
- **Not an attest unit.** The plan called for the name and summary to be
  attestable the way a page reading is. What shipped is the weaker,
  simpler form of the same rule: `gen_source='person'` supersedes and is never
  regenerated. It is not content-addressed, it does not accumulate versions, and
  a correction does not appear in the document's history. Fold it into
  `page_readings`/`attestations` when identity has to answer "what did this
  caption say when that packet cited it".
- **Re-generation policy.** A corrected transcription should probably invalidate
  a caption derived from the old one. Nothing does that yet — `raglit identify
  --force <doc>` is the manual answer.
- **The pool recipe does not include the identity model.** A pooled document
  captioned by model A is reused as-is when config names model B. Not silent —
  `gen_model` records which model wrote it — but it means a model change does not
  re-caption a corpus on its own.
- **The slot budget is per-worker, not global.** Identity holds itself to two
  concurrent calls, and the ingest pipeline's OCR calls are outside that count —
  so a sweep running alongside a big ingest can still put three or four requests
  at a two-slot server. The real fix is one semaphore over every model call in
  the process; until then, drop `identity_slots` to 1 while ingesting.
- **Nothing measures the endpoint's concurrency.** Two is configuration, taken
  from what the server is known to run. A wrong number is silent.
