# The concept map

raglit has accumulated several ideas that all sound like "this document is
related to that one" and are not the same idea. Written down because they are
individually reasonable and collectively confusing, and because the confusion is
the expensive kind: choosing the wrong one records a claim the corpus will act
on later.

Three questions separate everything here.

1. **Is it MEASURED or RULED?** A measurement is recomputed from the bytes and
   is never wrong, only imprecise. A ruling cannot be recomputed and is
   somebody's decision.
2. **What does it CLAIM?** Several things share a shape and assert different
   facts. Shape is not meaning.
3. **Where does it LIVE?** Derived storage is rebuilt at will. Durable storage
   is the record.

---

## 1. Measurements — what the text says

Computed by `similar.go` from shingles and alignment. Recomputed on demand,
stored only as a cache. **None of these is a claim about what two documents
ARE.** They are the evidence a person rules on.

| value | means |
|---|---|
| `identical` | the two folded texts are the same |
| `duplicate` | each is substantially the whole of the other |
| `probe-inside-match` | the probe occurs whole inside the match |
| `match-inside-probe` | the reverse |
| `overlap` | shared passages, neither contains the other |

Reported alongside: `jaccard`, containment both ways, block coverage, the page
alignment, the gaps, and the numeric tokens that differ.

**The trap.** `duplicate` looks like a verdict and is not one. A re-recorded
deed and a clean second scan of one deed both measure `duplicate` at 0.97. Two
failed OCR reads that returned nothing but the same watermark also measure
`duplicate`, at 1.000 — that happened here, on a 2021 deed of trust and a 2024
railroad agreement that share no content whatsoever. A measurement cannot tell
you what a pair is. That is the next section's job.

## 2. Rulings — what the documents ARE

Recorded in the judgement store. Each asserts something different, and the
differences are load-bearing because kgraph acts on them.

| relation | claim | decided by |
|---|---|---|
| `copy` | **same document**, no substantive difference | machine for byte-identity; human otherwise |
| `version` | **same document**, differs, one may govern | human |
| `unrelated` | the overlap means nothing — shared forms, a quotation | human |
| `seen-in` | this document was OBSERVED inside that one | machine where the container says so; human otherwise |
| `after` | this document PRESUPPOSES that one | machine for email headers; human elsewhere |

**`copy` and `version` are both about the SAME document.** That is what stops
everything else collapsing into them. A forward is not a version of the message
it quotes — it is a different message. An attachment is not a version of the
archive that carried it. Part 2 is not a version of part 1.

**`copy` is the same document RENDERED differently** — a different orientation,
zoom, resolution or scan quality. Nothing about the instrument changed; only the
picture taken of it did. This has a consequence the implementation has not
caught up with: two renderings of one deed can measure LOW on text similarity
because their OCR disagrees, and they are still copies. The 2008 lot
certification's reproduction of the operative record of survey aligns at 0.589 —
not because the instruments differ but because that scan reads "LAURENCE
MOONION" for Clarence Brannock. Text coverage is evidence for `copy` and is not
the definition of it.

**`version` is the same document CHANGED** — and should carry a date and a
reason. A version with neither is a pair somebody will have to re-derive later
from the documents themselves, which is the work the ruling existed to save.

**`version` asserts supersession and has teeth.** kgraph reports "every fact
resting on it rests on a version that no longer governs." That is right for a
re-recorded deed and wrong for an email: what someone wrote on a date is what
they wrote, and a reply quoting it three days later supersedes nothing. This is
why `after` exists as its own relation rather than as a branching `version`.

**`after` means PRESUPPOSES, not merely "later".** The successor could not exist
without the predecessor: a reply needs the message it answers, part 2 needs part
1, an amendment needs the agreement it amends. Two documents that merely happen
to be dated in order are not `after` each other — that is chronology, which the
dates already carry.

**`after` is a DAG, not a line.** Two documents `after` the same predecessor is
a branch; branches of branches are a tree. Nothing special is needed for that —
but nothing about a branch implies one side wins, which is exactly the
difference from `version`. For two undated drafts "which governs?" is an open
question worth surfacing. For two replies to one message it is a category error.

`after` deliberately does NOT record whether the successor contains the
predecessor. An email forward does; report part 2 does not. The overlap is
measured anyway, so `after` only has to make it explicable.

## 3. Structure — what a document IS made of

Three document shapes, three different readings. The distinction decides what
reading a file should produce.

| shape | example | what reading it yields | how parts are addressed |
|---|---|---|---|
| **composition** | a scan of a declaration + its exhibits | pages that ARE the instruments | `slice` — a page range |
| **container** | `.eml`, `.mbox`, an archive | a transcript that REFERS to separate documents | manifest page + `seen-in` |
| **chain** | an email thread | messages, each quoting its ancestors | `after` |

**Composition** — carving by page range yields the instrument itself, and
citation composes: page 28 of the child is page 28 of the exhibit as filed.
That is `raglit slice`. Child page numbers are always the PARENT's; renumbering
a child to 1..N makes a quotation uncheckable against the document as filed.

**Container** — holds separate documents whole, in their own encodings. Its own
text only refers to them (`Attachments: legal.pdf (application/pdf, 5.6 MB)`).
Nothing composes: the enclosed document has its own bytes and its own
pagination wherever it was extracted to. Page 1 is a manifest of what is
carried, so "what is in here" is answerable without reading all of it.

**Chain** — each message contains its ancestors by quotation. The containment is
real and is not duplication to be cleaned up; it is the shape of the artifact.

### `seen-in` — a reference that points IN

An ordinary reference points OUT: this document cites that one. `seen-in` points
the other way — this document was OBSERVED INSIDE that one.

It is deliberately about observation rather than mechanism. A deed can be seen
in a title commitment because it was photocopied into it; an attachment can be
seen in an email because MIME carried it; an exhibit can be seen in a
declaration because it was bound behind it. Those are three different physical
facts and one useful claim, and a corpus assembled from a broker's file, an
iCloud export, a transaction file and a county record needs the claim far more
than it needs the mechanism.

**It does not mint an identity.** That is what separates it from `slice`. A
slice DECLARES that a page range is a document, creating something citable in
its own right. `seen-in` relates two documents that already exist. When the
thing inside is already held standalone, `seen-in` is all that is wanted and it
is cheaper: no child document, no materialisation, no second copy of the text.

Reach for `slice` instead when the thing inside is NOT held separately and needs
to become citable — pages 9-14 of a scan that exist nowhere else.

The two compose. Slice a bundle, then rule the child a `copy` of a standalone
instrument; or skip the slice and record `seen-in` with the page range as its
evidence. The first gives the enclosed instrument its own identity; the second
just records where it was spotted.

## 4. Storage — what survives what

| layer | rebuilt by | lives |
|---|---|---|
| source document | never — it is the evidence | the corpus |
| `raglit-audit.jsonl` | never — it IS the record | the corpus, git-tracked |
| `judgements.db` | `marks --rebuild`, from the trail | the corpus, gitignored |
| index (`index.sqlite`) | re-ingest | `~/.raglit/indexes/<project>__<name>` |
| `*.raglit-transcription.md` | every read | beside the document |
| `*.raglit-regions.json` | every region read | beside the document |
| page/OCR cache, shingles, pool | re-ingest, `similar --build` | the index |

**The rule.** Anything a person decided goes in the audit trail, because nothing
can recompute it. Everything else is derived and may be deleted at will. The
database is a projection of the trail — delete it, replay, and it is identical.

**The trap.** The index is NOT where your data lives. It sits outside the folder
Syncthing replicates and is gitignored in every project that has one. A ruling
written there is single-machine and dies at the next reindex.

## 5. Descriptions — what a page shows

For a page that is mostly drawing, the text is not the content.

| mechanism | produces | when |
|---|---|---|
| `[FIGURE: ...]` | an inline caption for a figure embedded in a text page | during OCR |
| region root reading | an ACCOUNT of the whole sheet — every drawing, table, legend, block, and what each shows | `raglit regions` |
| region descent | the characters in a named crop | `raglit regions --depth N`, opt-in |

**The distinction.** A figure caption assumes figures are discrete objects
inside prose. On a plan sheet the entire page IS the drawing, and the caption
model described a record of survey as "a vicinity map showing a grid of
sections" — true of one inset, silent about the survey. The region root is asked
a different question: account for everything here.

### A page is not either/or — it is a fraction

A photograph is wholly described and an order is wholly transcribed, but a
SCREENSHOT is both. chandra reading a page of SMS messages transcribes the
messages and narrates the phone around them: status bar, app icons, microphone
and camera buttons.

	measured on the delano SMS exhibit 2026-08-15
	  15 pages, all read by the VLM, 15% of the document described
	  per page 0% to 28% — 13 of 15 pages carry some, 2 carry none

Two measures, and they answer different questions:

| measure | question | shape |
|---|---|---|
| `IsDescribedPage` (≥90%) | may this be quoted as the record? | binary, and stays binary |
| `described_chars/text_chars` | how much of it did a model make up? | graded, per page |

**Where it is measured is the whole constraint.** The evidence is the layout
markup (`data-label="Image"`, `<img alt>`) and the index holds the FLATTENED
text, so the fraction can only be taken in `ingestUnits` before the strip, and
must be stored. It was being recomputed downstream from the indexed text, where
it could only ever return 0.

**The method is what READ the page, not what chopped it.** It was taken from the
fragmenter's mode, and `text-overlap` is also what a vision read falls back to
when the LLM segmenter drops text — so a 15-page exhibit read entirely by
chandra was recorded as `text-layer`, which does not merely mislabel it, it
RAISES the claimed trust from a model's 90 to an exact 100. It now comes from
`ocr_pages.engine`, and any vision page makes the whole document a vision read:
a reader quoting a document has no way to know which page a sentence came from,
so the weaker claim governs.

### The pool replays a document without the evidence for it

Cross-index reuse copies fragments, vectors and page images into the next index
with no OCR and no model — so anything the FIRST read established has to travel
in the payload or it is gone, and cannot be recomputed, because the layout
markup it came from is not in the payload at all.

Three things did not travel, and each made the reused copy claim more than the
original:

| lost | consequence |
|---|---|
| `fragments.origin` | a photograph's description arrived UNMARKED — a model's "a red Chevrolet Malibu, licence plate CEP0912" indexed as quotable record in every index but the first |
| per-page described counts | the reused copy could not be scored at all |
| the reading itself | the pooled path never recorded one, so trust depended on which index read the file first |

All three now travel. Payloads pooled before the counts existed read back as 0,
which is why `DescribedUnmeasured` (-1) exists: unmeasured is not zero, and
recording it as zero asserts "a model made none of this up" about exactly the
documents most likely to be screenshots.

**Does the pool cache failures?** Not hard ones — `Pool.Put` runs only when the
ingest returned no error. It does cache PARTIAL reads: a document where some
pages failed and at least one page read is committed as a success (deliberately
— "some content beats none, none is a failure"), with `engine='failed'`
provenance rows, and that is what gets pooled and replayed. Measured on the
dogfood pool: 2651 entries, 2 carrying a failed page, 38 with no fragments at
all. Small, and not the cause of the reading gap — that was simply an early
return ahead of the recording call.

**Known gap, not fixed.** Only 14 of 657 documents in the delano index carry a
reading at all — readings are recorded at ingest, and most of the corpus predates
them. 342 documents have vision pages and no reading. They cannot be backfilled
honestly: the described fraction needs markup that no longer exists, so a
backfill would record `0% described` for every one of them, which is the same
false claim in a new place. The only honest repair is a re-read.

### The cheap OCR tier cannot serve a corpus of drawings

Reading a page has three tiers: the PDF text layer (no model), a cheap OCR
engine (`tesseract` or `paddleocr`, no LLM), and the vision model. The cascade
exists to keep the expensive call rare, and for scanned prose it works — a
declaration, a letter, an order all read fine on the cheap tier.

**It cannot work for a page whose content is a drawing, and the reason is not
that OCR is bad at drawings.** Tesseract will happily return the labels,
bearings and lot numbers off a plan sheet. What it cannot return is what the
sheet DEPICTS, because a description is not a transcription — it is an answer to
a question nobody asked the glyph recogniser.

The trap is in the escalation gate. `GibberishConfig` decides when a cheap read
is untrustworthy enough to re-run on the vision model, and it is deliberately
precision-biased: mean recogniser confidence, fraction of word-like tokens,
"only clearly-bad pages (handwriting, degraded scans, garbled figures)". Every
one of those measures LEGIBILITY.

A plan sheet read cheaply is legible. Short labels, clean print, high
confidence. It passes the gate, does not escalate, and is recorded as a
successfully read page — with no description of the drawing that is the entire
content of the sheet. The gate asks "is this text trustworthy?" when the
question that matters here is "does this page need describing?", and for a
drawing the first answers yes while the second also answers yes.

So on a corpus of recorded surveys, plats and exhibits, the cheap tier is not a
cost optimisation with a quality cost. It is a silent loss of the content, of
exactly the kind this file keeps cataloguing: no error, a page that reads as
complete, and the thing that mattered absent.

Two consequences worth stating plainly:

- **Diagram pages must reach the vision model**, whatever else is configured.
  Any cheap-tier rollout has to route them there by document class, not by
  legibility score.
- **`DescribeFigures` is off by default**, so a born-digital PDF carrying an
  embedded diagram reads its text layer, never escalates, and its figures go
  undescribed. That is the same loss arriving through the other tier.

**Root descriptions are not quotable.** A sheet read whole returns `HALVR` for
Halvor and drops digits from auditor file numbers. That is the division of
labour: the root says what is on the sheet, a crop says what it says, and
`kg attest` records what a person actually checked.

---

## Choosing

- Two files, one instrument, no difference → **`copy`**
- Two files, one instrument, differs, one may govern → **`version`**
- One file, several instruments printed together → **`slice`**
- One file carrying separate documents → **container**: manifest + **`seen-in`**
- A document you already hold, spotted inside another → **`seen-in`**
- One document that presupposes another → **`after`**
- Text overlap you have not ruled on → a **measurement**, nothing more

When two of these seem to fit, the question is which CLAIM you are making, not
which shape the evidence has. The shapes overlap; the claims do not.
