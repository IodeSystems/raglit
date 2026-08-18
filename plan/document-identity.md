# raglit: document identity — what a document IS, in its own words

Status: BUILT 2026-08-01 (`identity.go`, `identityqueue.go`, `raglit identify`,
`/api/identify`, `/api/identify/queue`).
TAGS + INDEX IDENTITY 2026-08-18 (`indexdigest.go`, `tagmerge.go`, `raglit about`,
`raglit audit-tags`, `raglit identify --tags-only`).
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
- `pipeline.go` — ingest does NOT call the model. A caption is downstream of the
  transcript, so `commitDoc` records that one is DUE, in the same transaction
  that commits the text, and the captioning queue does the rest. That edge fires
  on every re-read, which is what makes a document that had nothing to summarise
  get named the moment it does — the lead-based-paint disclosure indexed as its
  own signature stamp was skipped, and came back on its own when a real
  transcript replaced the overlay. `commitDoc` also re-emits the identity
  fragment inside the swap, so the columns and the searchable text can never
  disagree.
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

## Tags, and what an INDEX is (2026-08-18)

Two fields more, asked in the same call, and one level up.

- **`content_tags`** — 3–5 short noun phrases for what the document is ABOUT.
  OPEN vocabulary, because the subjects of a corpus are not enumerable in
  advance; bounded in SHAPE instead (1–3 words, ≤40 chars, lowercase, no
  commas — the column is comma-separated, so a comma in a tag comes back as two
  tags on the next read, silently and only for whichever documents got one).
- **`role_tags`** — 1–3 from a CLOSED vocabulary for what job the document does
  in the corpus: `documentation · reference · overview · specification · guide ·
  changelog · notes · report · data · other`. Closed for the same reason `kind`
  is, with `NormalizeRole` mapping the aliases a model emits.

Stored in `documents.gen_content_tags` / `gen_role_tags` and written into the
identity fragment, so they are searchable by the same path the summary is.

### Drift, and the two halves of holding it together

An open vocabulary drifts: "lead paint", "LBP" and "paint inspection" arrive
from three documents meaning one thing, and a tag nothing else repeats groups
nothing.

- **Prospective.** The identity prompt is seeded with the index's established
  vocabulary — `Store.TagContext`, the top 15 content tags by frequency — so a
  document is tagged in the terms the corpus already uses. It is a per-call
  PARAMETER, not a field on the Identifier: one `*Identifier` is shared by every
  index in a registry and by every slot of the captioning queue, so a field
  there is a data race AND carries one index's vocabulary into the next. The
  queue reads it per job in the loader (off a model slot), which is what makes a
  corpus captioned from empty converge on the terms it establishes as it goes.
- **Retroactive.** `raglit audit-tags` reports the drift that got through, with
  a `≈` list of tags sharing a significant WORD (whole words — substrings pair
  "data" with "metadata", and a proposal nobody trusts is a proposal nobody
  reads). `--merge "old,other=>new"` applies a collapse. Deliberately not
  automatic: whether two terms mean the same thing is not something spelling
  establishes — "lead paint" and "lead paint disclosure" share every significant
  word and are different facts. The report proposes; a person rules.

### The backfill

`raglit identify --tags-only` fills tags into documents that already have a
caption. Its own selector (`DocumentsMissingTags`) and its own ask
(`Identifier.IdentifyTags`, `emit_tags`), because the alternative — a `--force`
re-identify — rewrites hundreds of captions that are already right, a person's
among them. It writes the two columns and NOTHING else: the caption, its author,
and `gen_text_hash` are left exactly as they stand. The hash matters: stamping it
with text no caption was read from would claim the caption is current and silence
the staleness re-arm in `commitDoc`.

Same queue, same rows, same resumability — `identity_jobs.tags_only`, with the
opposite keep rule (an existing caption is the precondition; existing TAGS are
the reason to decline).

### What an index is

`identity.go` answers "what is this document". Nothing answered "what is in this
index", and the absence had a specific symptom: an agent searching a corpus for a
topic it does not hold gets an empty result, which is indistinguishable from a
badly phrased query — so it rephrases and searches again, several times, against
a corpus that was never going to have it. The index knew the answer and never
said it.

Two forms, because they fail differently (`indexdigest.go`):

- **The digest** — documents, kinds, top content and role tags, counted from
  what is stored. One query, no model, never stale. This is the one attached to
  an empty search (`covers`), scoped to the SAME subtree the search was: a
  whole-index digest shown for a path-scoped query claims coverage the subtree
  does not have.
- **The about** — a paragraph a model writes from the CAPTIONS (not the
  documents; the captions are already a model's account of them). Two paraphrases
  deep, so it is marked generated wherever it is shown, and stamped with the
  document count it was written from — a summary of 40 documents shown for an
  index of 400 is worse than none, and `about_stale` is what makes that
  detectable.

Surfaced by `raglit about [--write]`, `list_indexes`, and the empty-search
`covers`. Both MCP backings carry it: the digest was previously computed only in
the embedded server, which is not the default path — a daemon-routed agent, which
is most of them, saw none of it.

### The upstream rule this forced

A caption is downstream of the transcript, so captioning kept finding documents
that had no transcript — and the reason was always the same: raglit had decided,
from a PDF's text layer, not to READ the page.

Three fixes went into that decision before it was abandoned. Count letters not
spaces (a watermark padded to 144 characters by `pdftotext -layout`). Discount
lines that repeat on ≥80% of pages (an `Authentisign ID` stamp, 46 characters,
nearly twice the threshold). Notice a page-covering raster (the same stamp on a
one-page scan, where nothing repeats). Each was correct, each was measured, and
each missed the next case — including a 30-page purchase and sale agreement
signed under TWO envelopes, where neither stamp reaches 80% and ten pages,
Exhibit A among them, passed as text.

The pattern is the finding: a heuristic deciding whether to look at a page keeps
being wrong in ways nothing can detect, because the evidence that it was wrong is
the page it declined to look at. So raglit reads every page. The text layer
survives only when no OCR is configured at all, where the choice is between it
and nothing.

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
- `indexdigest_test.go`: the digest counts kinds/tags/untagged; it scopes to the
  same subtree a search does; `TagContext` is the established vocabulary and is
  empty on a fresh index; an About written from half the corpus reports stale.
- `tagmerge_test.go`: the `a,b=>c` spec and its refusals; a merge collapses
  across the index, deduplicates a document carrying both spellings, reports the
  tags nobody carried, moves the identity FRAGMENT with the columns, and touches
  neither the caption nor unrelated documents; `≈` matches whole words only; a
  tag cannot carry the separator it is stored with.
- `identityqueue_test.go`: the queue carries the index vocabulary into the
  prompt (the path that captions a corpus is the path where tags drift, and it
  was the one the mechanism was missing from); a tags-only job leaves the
  caption, its author, and its text hash alone, and declines a document that
  already has tags.
- `cmd/raglit/audittagscmd_test.go`: the audit reports the vocabulary, names the
  untagged, and marks `≈` as a proposal; `--merge` applies only what was named;
  `raglit about` prints the counted digest with no model configured.
- Live, against the configured endpoint: a text document ingested with `raglit
  index` came back captioned "2021-05-25 Purchase and Sale Agreement
  (Ardley/Brannock)", kind `agreement`; a person's `--name` superseded it; a
  re-ingest and a `--force` sweep both left the person's caption alone; the
  search hit on the summary printed marked.

## Open / not done

- **The `about` paragraph is not regenerated on its own.** It goes stale as the
  corpus grows and says so (`about_stale`), but nothing rewrites it — `raglit
  about --write` is the manual answer. A hook on the identity queue draining
  would be the obvious place.
- **Merges are not recorded.** `--merge` rewrites the tags and leaves no trace
  that a person ruled two terms equivalent, so the next corpus re-derives the
  ruling from nothing. The judgement trail (`raglit mark`) is the shape this
  should take if it matters.
- **`≈` is lexical only.** It cannot see that "escrow closing" and "settlement"
  are the same thing. A model pass over the tag list would propose those, and
  would still be a proposal.

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
