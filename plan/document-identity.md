# raglit: document identity — what a document IS, in its own words

Status: proposed 2026-08-01. Not built.

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
nobody can navigate, and the review UI now makes that plain: the Documents tab is
mostly indistinguishable rows.

## What to add

At the end of a read — the point where the whole transcript exists in one place
and has cost nothing extra to obtain — ask for three things and store them:

- **`name`** — what a person would call this filing. `2021-05-25 Form 21
  purchase and sale agreement, executed (Ardley/Brannock)`. Not a slug, not a
  filename; a caption.
- **`summary`** — what the document is and what it covers, in a few sentences.
  The instrument, the parties, the date, the ground it concerns, what it does.
- **`kind`** — deed · survey · agreement · correspondence · court filing ·
  certification · analysis. A small closed vocabulary, because an open one
  produces forty spellings of "letter".

Stored on `documents` (new columns), and **indexed as a fragment** so the summary
is searchable text like any other. That is the part that fixes discovery: a query
for "purchase and sale agreement" then matches a document whose body never says
those words in a form BM25 can rank, because the summary does.

## Design notes, and the traps

**It is a machine claim, and must be labelled as one.** A generated name is a
reading, exactly like a transcription — so it belongs in the same discipline that
already exists for readings: it is attestable, correctable by a person, and a
correction supersedes rather than overwrites. `page_readings` and `attestations`
already do this; a name and summary are one more thing a person can rule on. A
generated caption presented as fact is how "Lead-Based Paint" happens again in
the other direction.

**Never rename the file.** The path is the identity everything else joins on —
fragments, page images, region trees, readings, verdicts, the audit trail, and
every citation already written into a legal packet. The generated name is a
DISPLAY name and a search target, nothing more. The stored filename stays, and
both are shown, because "this file is called X and is actually Y" is itself the
finding.

**Summaries must not be searched as if they were the document.** A hit on a
generated summary is a hit on a machine's paraphrase, and a person citing it has
cited nothing. Fragments carry their origin already; the summary fragment needs
to be marked so search results can say so, and so a quotation tool refuses to
quote from it.

**Cost.** One extra model call per document at ingest, on the assembled text
rather than per page. Cheap next to OCR. It should be skippable
(`--no-identity`) and re-runnable on its own for a corpus already indexed, since
the ardley index is 406 documents that will not be re-OCR'd to get captions.

## Open

- **Does the summary reach the embedding?** Searchable-as-text is clearly right.
  Embedded as a vector is arguably better for recall and arguably pollution —
  a paraphrase competing with the document's own words. Decide by measuring on
  the ardley corpus, not by argument.
- **Re-generation policy.** A corrected transcription should probably invalidate
  a name derived from the old one. That is the same supersede-not-overwrite
  question `page_readings` answers, and the answer is probably the same.
- **`kind` vocabulary** should be settled before anything writes it, or it will
  be settled by whatever the first model returns.

## Where it plugs in

- `extract.go` / the ingest pipeline — after the transcript is assembled.
- `documents` table — `gen_name`, `gen_summary`, `gen_kind`, plus provenance
  (model, when) so a caption can be told from a human one.
- `/api/documents` and `/api/doc-detail` — return both names; the review UI shows
  the generated one with the filename beneath it.
- Search — the summary as a marked fragment.
- attest — the name and summary as attestable units, so a person can correct a
  caption and have that stick.
