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
| `carried-by` | arrived INSIDE a container | machine — the archive says so |
| `after` | follows in a sequence; supersedes nothing | machine for email headers; human elsewhere |

**`copy` and `version` are both about the SAME document.** That is what stops
everything else collapsing into them. A forward is not a version of the message
it quotes — it is a different message. An attachment is not a version of the
archive that carried it. Part 2 is not a version of part 1.

**`version` asserts supersession and has teeth.** kgraph reports "every fact
resting on it rests on a version that no longer governs." That is right for a
re-recorded deed and wrong for an email: what someone wrote on a date is what
they wrote, and a reply quoting it three days later supersedes nothing. This is
why `after` exists as its own relation rather than as a branching `version`.

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
| **container** | `.eml`, `.mbox`, an archive | a transcript that REFERS to separate documents | manifest page + `carried-by` |
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

**Root descriptions are not quotable.** A sheet read whole returns `HALVR` for
Halvor and drops digits from auditor file numbers. That is the division of
labour: the root says what is on the sheet, a crop says what it says, and
`kg attest` records what a person actually checked.

---

## Choosing

- Two files, one instrument, no difference → **`copy`**
- Two files, one instrument, differs, one may govern → **`version`**
- One file, several instruments printed together → **`slice`**
- One file carrying separate documents → **container**: manifest + **`carried-by`**
- One document following another → **`after`**
- Text overlap you have not ruled on → a **measurement**, nothing more

When two of these seem to fit, the question is which CLAIM you are making, not
which shape the evidence has. The shapes overlap; the claims do not.
