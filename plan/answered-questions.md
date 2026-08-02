# raglit: the questions a document answers

Status: PROPOSED 2026-08-02. Extends `plan/document-identity.md` — same
mechanism, one more origin.

## Goal (from user)

> "take a document and have a summary, and state what questions this document
> answers, potentially with fragment pointers … a single memory document, we can
> add each memory as fragments, and the summary gets rebuilt, as well as the q&a
> pointers"

> "a question is created with the answer only — and might need a justification
> such that one can quickly find the reference in the document — there can be
> multiple references per answered question"

## Why this is one more origin, not new machinery

`identity.go` already indexes a machine-written paraphrase as its own fragment,
`origin='identity'`, ranked in the same list as the document's own words. The
reasoning is written down there and it is exactly the reasoning for questions:

> "the summary says the words the body never says in a form BM25 can rank … it
> is a machine's paraphrase, so every renderer says so and nothing quotes from
> it."

A question is the same trick aimed at the query side. A searcher does not phrase
their need as the document phrases its content; they phrase it as a QUESTION,
and an index of questions matches that shape directly. So: `origin='question'`,
generated where identity is generated, held to the same discipline — a machine
claim, marked as one, never quoted as the document's words.

## The rule that makes it safe: a question is derived from an answer

A question is generated FROM an existing fragment, never authored on its own
(USER). This is the whole safety property, and it is structural rather than a
matter of care:

- An aspirational question — "How does the TUI work?" against a fragment that
  covers a third of it — is WORSE than no index entry. It is optimised to win
  the retrieval, so it wins, and then disappoints: it costs a read AND teaches
  the caller to distrust the index.
- Deriving the question from the answer means a question cannot exist without
  something that answers it. The failure mode is not mitigated, it is
  unreachable.

## References: one question, many places

A question carries 1..N references into the document (USER), because the answer
to "what did the parties agree about escrow" is rarely one fragment. A reference
must be enough to JUMP to — the point is that a reader lands on the passage, not
that they re-search for it.

`Hit` already returns `Page` and `Ord`, so a single reference costs nothing:
co-locate the question fragment at its primary answer's coordinates and the
pointer is implicit. Multiple references need somewhere structured to live —
a `refs` column on the fragment row is preferable to encoding them in the
searchable text, which would pollute what the embedding sees with coordinates
nobody is searching for.

Open: whether the justification is a short quoted span per reference (jump
target plus evidence, costs storage) or the coordinates alone (cheap, makes the
reader open the document to find out why). Lean quoted-span: a reference the
reader has to verify by hand is a reference they will stop following.

## Rebuilds

Re-ingest is already idempotent — `Ingest` drops a document's fragments and
replaces them — so "add a memory, rebuild the summary and the questions"
converges rather than duplicating. One rule:

**Rebuild from the FRAGMENTS, never from the previous summary.** A summary of a
summary compounds its own errors silently, and the full fragment set is cheap to
re-read. The same holds for the question list: regenerate it against the
fragments that exist now, so a deleted memory takes its questions with it.

## The operation set is `attest`'s, already (USER, 2026-08-02)

create · refine · delete · attest — and every one of them is an existing Kind in
`attest/attestation.go`. Nothing new is needed but the decision to point it at
question units:

| operation | kind | note |
|---|---|---|
| refine an answer | `corrected` | the correction IS the record, not a rewrite |
| delete a void question | `retract` | appended, never a row removal |
| a person checked it | `confirmed` | the escalation, for a load-bearing answer |
| a person read past it | `affirmed` | the ordinary pass |
| the source does not support it | `unsupported` | the answer was invented |
| cannot tell | `unclear` | categorical, never a score |

Append-only, one JSON object per line, for the reason already written down: a
mutable verdict file answers "what is the current ruling" and DESTROYS the record
of how it was reached. A memory that was corrected twice and then confirmed is a
different object from one that was simply written correctly, and only the log
can tell them apart.

## Orphaning is the staleness detector — and it already exists

This is the part that closes the problem `dun/plan/icebox.md` records as
unsolved. Unit ids are content-addressed, the log is SEPARATE from the reading
and is never rewritten by a re-read, and `attest/state.go` reports
`Orphaned []Entry` — a verdict whose unit no longer exists is surfaced rather
than quietly re-attached to a claim nobody ruled on.

Point that at questions and staleness detects ITSELF. Address a question unit by
its content — the question plus the answer it was derived from — and a rebuild
that changes the answer produces a NEW id, orphaning every attestation against
the old one. "This was confirmed, and then it changed" stops being something
anyone has to notice: it is the difference between two sets, computed on
reingest.

The two halves are complementary, not redundant. Orphaning catches what changed
underneath a verdict; the correction path in the rendering (below) catches what
was wrong from the start and never changed. Neither finds the other's case.

## Risks

- **The summary launders a wrong fragment into an authoritative claim.** A bad
  memory is one deletable fragment; a bad SUMMARY reads as the document's
  considered view. Fragments should carry provenance (who wrote it, against
  which commit) so a wrong claim in the summary is traceable to its cause.
- **Cost per append.** Every new fragment implies a regeneration. Batch, or
  regenerate on read rather than on write — an index nobody has searched since
  the last append has not yet needed to be correct.
- **Question drift.** Questions generated against fragment N are not re-derived
  when fragment N changes unless the rebuild covers them. They must be part of
  the same regeneration, not a separate pass that can fall behind.

## Consumer notes

Two things belong to the CONSUMER rather than to an index several consumers
share (see `dun/plan/icebox.md`):

- **"Already surfaced, do not propose again"** is read-state, and read-state is
  per-consumer. raglit staying stateless about its readers is what lets two of
  them suppress independently.
- **The caveat travels with the rendering.** Whatever surfaces a
  machine-written fragment says that it is one, that it may have gone stale, and
  how to correct it — the correction path is the only thing that makes staleness
  NOTICEABLE, because it puts the question in front of a reader at the one
  moment they are holding the evidence to answer it. raglit already holds half
  of this rule for `origin='identity'` ("every renderer says so and nothing
  quotes from it"); the other half is that a renderer must also say how to fix
  what it is showing.
