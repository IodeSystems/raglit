# raglit: corrected-by — what supersedes what, and at which grain

Status: proposed 2026-08-01. Not built.

## Goal (from user)

> "do we have a concept of 'corrected-by'? … so, a version can be refuted, OR the
> document can be corrected. either way … I guess corrected can be version or
> whole document."
>
> "perhaps refuted is too specific, corrected-by or refined-by?"
>
> "refined is bad. corrected-by and invalidated-by makes more sense."

## What exists today

Three things, and each answers a narrower question than this one:

- **`page_readings`** — seq + active. Within ONE document, a person's correction
  supersedes a machine read and the old text is kept. This is corrected-by at the
  READING grain, and it works.
- **`attest.Corrected`** — a verdict on one unit, in an ordered log. Corrected-by
  at the CLAIM grain, derivable from log order.
- **`MarkVersion` + `Supersedes`** — a person's ruling that two documents are
  versions of one instrument, and which governs.

The gap is the third one. `Supersedes` only means anything inside `MarkVersion`,
and version means *the same instrument twice*. It cannot say that one instrument
corrects a **different** instrument, which is the ordinary case in a records
corpus and the one this project keeps hitting.

## The case that shows it — AF 202205230090

The 2022 Halvor record of survey states, on its own face:

> "THIS SURVEY IS A CORRECTION TO THE BOUNDARY OF LOT 'I' … ORIGINALLY RECORDED
> UNDER HAVERN COUNTY AUDITOR'S FILE NO. **200808180120**"
>
> "THIS SURVEY HAS CORRECTED THE INTENDED LOCATION OF LOT 'I' WHICH IN FACT IS 25
> FEET EAST BASED UPON … FIELD WORK AND DEED RESEARCH"
>
> "ADDITIONALLY, THE SURVEYOR'S NOTE SHOWN ON THE PREVIOUS [survey] RECORDED UNDER
> AUDITOR'S FILE NO. **202203010021** IS N[OT correct] WITH RESPECT TO THE
> NEIGHBOR'S SHED"

Two different relations in one sheet:

1. **It corrects a different instrument.** The 2008 Summit survey is not a version
   of the 2022 Halvor survey — different surveyor, different year, different
   document — and 2022 says 2008 put the boundary in the wrong place. That is a
   claim of ERROR IN a standing instrument, so `corrected-by` — the 2008 survey
   still created the lots. `MarkCopy` is wrong, `MarkVersion` is wrong,
   `MarkUnrelated` is wrong; the pair currently computes as `overlap` and rules as
   nothing.
2. **It corrects ONE NOTE of another document.** Not the whole of AF 202203010021 —
   one surveyor's note about a shed. Nothing in the vocabulary attaches to a part,
   and note that the rest of that survey is untouched by it.

Both matter to the matter: which survey states the boundary correctly is the
dispute, and a corpus that cannot record "this one corrects that one" leaves the
answer in a person's head.

## What to add

A relation with **direction** and a **grain**, separate from the same-instrument
question `MarkCopy`/`MarkVersion` answers.

- **`corrected-by`** — the later fixes something IN the earlier. The earlier
  still stands as an instrument; a specific thing in it is wrong and the later
  states the right answer. The 2008 survey still created the lots — it is wrong
  about where Lot I sits.
- **`invalidated-by`** — the earlier no longer governs AT ALL. Rescinded,
  vacated, voided, wholly superseded. Nothing in it should be relied on going
  forward.

Both directional: the pair is not symmetric and normalizing it away would lose
the whole content.

**These are a STATUS, not a quality judgment, and that is the point.** A title
examiner reading a chain needs to know two different things — "is this instrument
still good" and "is this particular fact in it still right" — and they have
different answers for the same document. An instrument can be riddled with
corrections and still govern; an instrument can be perfectly drafted and void.

**Why not `refutes`, and why not `refined-by`.** Both were earlier drafts of the
second kind and both were wrong in opposite directions.

`refutes` asserts FALSITY, which is too strong: a 2022 re-survey with better
instruments is not calling the 2008 surveyor a liar, and in a matter where one
surveyor's competence is genuinely at issue the tool must not put that word in
anybody's mouth.

`refined-by` is too weak, and worse, it is a judgment about QUALITY rather than a
statement about STANDING. "The later one is better" is an opinion; "the earlier
one no longer governs" is a fact about the record with consequences. A reader
asking which survey to rely on is not asking which is finer work.

The two chosen kinds are also the two that a person can be WRONG about in a way
that matters, which is the argument for making a person rule on them rather than
a machine.

**Grain** is the second axis, and it is what the user's "version or whole
document" names:

| Grain | Example |
|---|---|
| whole document | the 2022 survey corrects the 2008 survey |
| a part | the 2022 survey corrects one note of the 2022-03 survey |
| a reading | a corrected page reading supersedes a machine one — `page_readings`, already built |
| a claim | an `attest` verdict on one unit — already built |

The two already built should not be re-implemented; the new relation covers the
first two rows and should be able to POINT at a region, page or unit for the
second, using identifiers that already exist (region id, page number, attest unit
id).

## Where the claim comes from, and who is allowed to make it

**Proposed by reading, ruled by a person.** The Halvor survey says what it
corrects in plain English and names the file number. A document read that already
produces a summary (see `document-identity.md`) can equally produce "this
document states that it corrects AF 200808180120" — a candidate, resolvable to a
path because auditor file numbers are already how this corpus names things.

It must arrive as a PROPOSAL. "A corrects B" is a legal conclusion when the
documents are instruments of title, and a machine asserting it silently is the
same failure as a machine asserting a boundary. The existing split is right and
should be reused: the computation proposes (`Relation`), a person rules
(`MarkKind`), and the two are shown side by side.

## Traps

- **Do not normalize the pair.** `Mark` sorts A and B so a pair is one fact. That
  is correct for copy/version and destroys both new kinds, where which side is
  which IS the content. Store direction explicitly.
- **A corrected OR invalidated document is still evidence.** Neither may hide it,
  delete it from search, or grey it out. What the 2008 survey said is exactly
  what the dispute is about, and an invalidated instrument is often the whole
  reason a case exists.
- **`invalidated-by` is the stronger claim and needs the stronger guard.** Saying
  an instrument contains an error is ordinary; saying it no longer governs is a
  legal conclusion with consequences for anyone relying on the record. A machine
  may PROPOSE it from explicit words — "rescinded", "vacated by order of" — and
  must never infer it from supersession alone. Two surveys of the same ground do
  not invalidate each other by existing.
- **Chains.** 2008 ← 2022-03 ← 2022-05 is three surveys, each correcting the last.
  Whatever is stored has to answer "what is current" without flattening the chain,
  for the same reason `page_readings` keeps every version.
- **Partial corrections do not supersede the whole.** A note about a shed being wrong does
  not make the rest of that survey wrong, and a UI that greys out the whole
  document because one paragraph was corrected has misreported the record.

## Where it plugs in

- `relations.go` — the new kinds, with direction and an optional locator.
- The read — propose `corrected-by` / `invalidated-by` candidates from the
  document's own words, alongside the summary and name. The distinction is
  readable: "IS A CORRECTION TO" is one; "rescinded", "vacated", "of no further
  force and effect" is the other.
- `/api/doc-detail` — beside Seen-in, which today shows overlap and no meaning.
- Review UI — a document should say "corrects: …" and "corrected by: …" at the
  top, because that is the first thing a reader needs to know and currently the
  only place it exists is in the body text nobody has read.
