# Hierarchical regions — reading an image that does not fit in one look

Status: **built** (`regions.go`, `regionread.go`, `regionfilter.go`,
`regiondoc.go`, `raglit regions`, `raglit region`). Not yet wired into ingest —
the descent runs only when asked for. The 2026-08-03 measurements that shaped
what is here are archived in `plan/done/2026-08-03-what-a-transform-is-worth.md`;
what remains OPEN below is the turn-3 escalation loop, placeholder assembly, and
the ingest trigger. Opened 2026-07-29 after a recorded land survey produced a
confident, complete-looking transcription that had silently dropped the clause
the matter turns on.

## The failure this answers

`documents/records/200808180120` is a 27 × 36.7 inch survey sheet — 991 square
inches, 10.3× a letter page. raglit renders it whole at 200 dpi (5401 × 7345,
39.7 MP) and the vision encoder charges it **4011 image tokens**, against 3678
for an ordinary letter page. Measured:

| | area | image tokens | tokens / sq in |
|---|---|---|---|
| letter page | 94 sq in | 3678 | **39** |
| E-size survey | 991 sq in | 4011 | **4** |

The encoder's budget is per IMAGE, so a bigger sheet buys nothing. The survey is
seen at roughly 0.24× — six-point surveyor lettering falls below the patch grid.

What that produced was not a blank or an error. It was tidy prose that had
replaced the entire legal description with a one-line figure caption and
invented plausible auditor file numbers (`A#200308270057` for `AF#9308270057`).
The repetition guard fired twice on the page, and both times on text that
**genuinely recurs**: the monument callout `FND. R/C MOWRER (6/12/2002)` (23
repeats) and a bearing call `2'48" W / 48.44' / S 46°2` (30 repeats). The model
was not hallucinating from nothing — it was transcribing something real and lost
count, at a resolution where one instance is indistinguishable from the next.

Verified fix-in-principle: one letter-sized tile, rotated upright, at the same
200 dpi, recovered `THAT LIES WESTERLY OF THE [CENTERLINE] OF SAID RIGHT-OF-WAY
AND BETWEEN THE NORTHEA[STERLY] EXTENSIONS...` — the clause the whole-sheet pass
dropped. Same model, same dpi, same page. Only the field of view changed.

## Why a grid is the wrong shape

The obvious fix is uniform tiling. It works, and it is still wrong:

- **Most of a drawing is empty.** A 5 × 3 grid spends 15 requests to read a
  sheet whose text lives in four blocks and a legend.
- **Rotation is not a page property.** One 1300 × 900 cell of this survey holds
  at least four text orientations: `LOT D` horizontal, `S47°38'15"W` at ≈ −25°,
  `S43°01'21"E` at ≈ +65°, `EXIST. HOUSE` at ≈ 90°. Rotating the sheet fixes the
  description block and does nothing for the interior.
- **A grid cuts through text.** The verification tile clipped mid-word
  (`REPLAT OF BLO`, `NORTHEA`, `SOU`) because the cut had no idea where the
  paragraph was.

## The shape

Ask the model what is worth looking at, and keep an account of the whole at
every level.

At each node: give the model the region as an image, and ask for two things.

1. **What this whole region IS** — a description covering everything visible,
   at whatever fidelity this scale allows.
2. **Which sub-regions merit a closer look** — bounding boxes, each with a
   rotation hint and a reason ("dense annotation", "text block", "title block",
   "table").

Then recurse into the proposed sub-regions. Stop when the model proposes none,
when a budget is spent, or when a region is small enough that another descent
buys no resolution.

The result is a tree whose ROOT says "a record of survey showing parcels A, B
and C in Havern County" and whose LEAVES carry exact text. Both are true, both
are useful, and they are useful for different queries.

### Why the description at every layer matters

It is the reason this is a hierarchy rather than adaptive tiling. Nothing
depends solely on descent being right: if the model proposes a bad region set,
the parent's description still records what was there. A missed region degrades
detail, not coverage.

It also gives retrieval something a tile cannot. A query for "record of survey"
matches the root; a query for a bearing matches a leaf; and the leaf knows its
ancestry, so a hit can be reported as *sheet → drawing interior → lot C corner*
with a crop to show.

## Data model

A region, not a fragment. Fragments are prose with an order; a drawing has
neither.

```
region(
  id, doc_id, parent_id,
  name,                                    -- slug, unique among SIBLINGS
  page, bbox_x, bbox_y, bbox_w, bbox_h,   -- in page coordinates, not tile
  rotation,                                -- right angles only; applied before OCR
  filter,                                  -- '' | contrast | sharpen; part of the render
  scale_dpi,
  kind,                                    -- content type, closed set — see below
  text,                                    -- transcription or description
  flags,                                   -- see below
  depth
)
```

`name` is a slug the reader assigns — `drawing-interior`, `existing-corners` —
and the path composes across levels, so a child refines its parent's name for
free. It exists because `id` explicitly does NOT survive a re-read: "a second
read of the same sheet proposes different regions and renumbers." A name is what
makes two reads of one sheet diffable. Unique among siblings, not globally;
global uniqueness is the path's job and enforcing it directly forces ugly
disambiguation.

There is no separate `title`. A name plus a description covers it, and a third
string invites two of them to disagree.

`text` stays on EVERY node, interior ones included. That is not redundancy, it is
the coverage guarantee: a bad region set must cost detail, not coverage, and the
2026-08-03 MinerU descent is what that looks like when the guarantee is absent —
its leaf never resolved the EXISTING CORNERS table out of the figure block, and
nothing in the tree recorded that the table was in there.

`kind` is a closed set, not free text: `text`, `table`, `drawing`, `diagram`,
`chart`, `title-block`. An open string means two readers name the same thing
differently and routing decisions get made on a coin flip.

Deliberately NOT on the record:

- **`skew`** — no page in the corpus has any, and 1-2° of it costs nothing when
  introduced deliberately, including on the page at the resolution limit. What
  the attempt to measure it DID find is that render geometry perturbs which
  marginal glyphs survive at all; see the measurements below.
- **`filters[]`** — built 2026-08-03, but as a single `filter`, not a chain:
  `contrast` (CLAHE clip 2.0, 8x8) or `sharpen` (unsharp σ1.0 α0.4). Those two
  settings are the ones measured to recover a fact, and only one may apply,
  because stacking two that each work recovers nothing. It is part of the RENDER
  — the digest covers the filtered bytes, so a repaired region only re-renders
  with its repair — and it is reached only through the protocol below: measured,
  proposed, judged.

Leaves are what get embedded and searched. Interior nodes are searchable too,
carrying their overview text. `bbox` in PAGE coordinates so any node can be
re-rendered from the original without replaying the descent.

The existing per-page image cache already keys on the SHA of the image bytes, so
every region — being an image — is cached by construction and a re-run costs
nothing for regions that have not changed.

## The protocol — who tells who what

Four parties, and the point of writing it down is that each one is barred from
deciding things the others are better at. Every boundary below was drawn because
a measurement said the other side gets it wrong.

**The RENDERER** (pure code, no model). Crops to a bbox, rotates by a right
angle, applies at most one filter, encodes a PNG, digests it. It decides nothing.
It is the only party that produces an image, so the digest it takes is what makes
any later claim about "the image the words came from" checkable.

**The MEASUREMENT** (pure code, before any call). Reports what is wrong with the
PIXELS, as flags: `low-resolution` from tokens per square inch, `blurred` from
the variance of the laplacian, `faded` from the 1-99 percentile spread. It runs
on the full-resolution crop.

> It is NOT allowed to decide that a repair happens. It cannot tell a faded fax
> from a drawing that is mostly bare paper, and a sparse crop of a clean sheet
> scores low without being blurred.

**The READER** (the model). Is shown the rendered image AND told what was
measured about it. Answers with an account of the whole region, a kind, and
proposals — sub-regions worth a closer look, each with a rotation, plus repairs
to try on this same area.

> It is NOT allowed to decide that the pixels are damaged. Measured: shown a crop
> blurred at sigma 1.6 it answered `skew` and `noise` at 0.9 confidence and
> prescribed a deskew — the one repair that makes a blurred region worse. Its
> confidence reads 0.9-1.0 whether it is right or wrong, so it cannot be gated on.
>
> It is also NOT allowed to decide whether a proposal is a descent or a
> transform. It routinely proposes the region it was just given as a "sub-region".

**The ROUTER and the JUDGE** (pure code). The router classifies each proposal by
GEOMETRY — materially smaller is a descent, same area at a new rotation or filter
is a transform, same area rendered identically is refused — and neutralises a
filter name that is not implemented. The judge decides whether a transform earned
its place: a degenerate render never wins, then a cleared flag, then agreement
with what the parent said is here, then distinct content.

> Neither is allowed to decide what a region SAYS. They never look at meaning,
> only at geometry, at flags, and at whether text repeats itself.

### The exchange, one node

```
  renderer  →            crop(bbox, rotation, filter) → png, sha256
  measure   →            low-resolution? blurred? faded?          [no model call yet]
            → reader     the png
                         + "MEASURED ABOUT THIS IMAGE: its strokes measure as
                            SMEARED. These are measurements of the pixels, not a
                            judgement about the document. If a repair would help,
                            propose THIS SAME AREA with a filter of contrast or
                            sharpen. Propose nothing if it reads fine."
  reader    →            {description, kind, regions:[
                            {x,y,w,h, rotation, filter, kind, reason}, ...]}
  router    →            by geometry: descents | transforms | refused
  ── for each transform ──
  renderer  →            re-crop with the proposed rotation/filter
  measure   →            did the flag that justified it clear?
            → reader     the repaired png (same exchange, one level of budget spent)
  judge     →            degenerate? flag cleared? agrees with the parent's
                         account? more distinct content?  → adopt or `cycled`
  ── for each descent ──
            → reader     the child crop, and the PARENT's description as `expect`
  measure   →            child shares nothing with `expect`? → `transform-suspect`
```

`expect` is the one thing threaded DOWN the tree rather than computed at the
node, and it is what lets a child indict its parent's transform: a rotation
applied the wrong way round does not garble a reading, it returns a reading about
somewhere else. Recorded, not acted on — correcting it means re-rendering the
parent, and the child cannot do that.

### A complex sheet, turn by turn

Status: DESIGN. Turn 1 and turn 2 exist; the placeholder assembly and the
escalation turn do not.

A simple page never reaches this. It is one turn: the sheet comes back with no
flags and no proposals, and that is the document. The turns below are what a
sheet costs when the first one says it is not enough — so the triage is not a
separate decision, it is the absence of a reason to continue.

---

**TURN 1 — the sheet.** Asked once, of the whole page.

*Given:* the page rendered whole; whatever `DamageOf` measured about those
pixels; the caller's hint if there is one.

*Answers:*

```json
{ "description": "... prose, with {{region:existing-corners}} where a named
                  area is too small to read at this scale ...",
  "kind": "drawing",
  "regions": [ {"name":"existing-corners", "x":0.10,"y":0.11,"w":0.76,"h":0.80,
                "rotation":270, "filter":"", "kind":"table",
                "reason":"monument table, lettering below this scale"} ] }
```

*Result choices, and what each costs:*

| the sheet came back | meaning | next |
|---|---|---|
| no regions, no flags | it is a page of text | **DONE — one call** |
| no regions, but flagged | it cannot see its own problem | tile it (`tileRegion`) |
| regions, each named | it knows where it cannot read | turn 2 per region |

The description is written with the named regions as PLACEHOLDERS, and the
parent's own attempt at each one is kept behind the placeholder rather than
discarded. That is what stops descent from becoming load-bearing: if turn 2
fails, assembly falls back to what the sheet said, and the span records which
level it came from. A hole in a document is worse than a blurry sentence in it.

---

**TURN 2 — a region.** Asked once per named region, at that region's own
rotation and filter.

*Given:*

- the region CROPPED and transformed — the pixels, at the resolution the sheet
  never had;
- what was MEASURED about those pixels (`blurred`, `faded`, `low-resolution`);
- the EXPECTATION — one line, what the sheet said is in here;
- the sheet's STRUCTURE — the placeholder map, so it knows where it sits and
  what is adjacent.

*Deliberately NOT given: the sheet's character-level transcription of THIS
region.* It is a reading taken at four tokens per square inch — the same class
of artifact as tesseract's numbers, and measured, "handing the model tesseract's
numbers makes it adopt them over its own correct reading, and an instruction to
prefer the image does not stop that." The child is looking at pixels that would
have told it the truth. Do not hand it the parent's invention. If context
demands the parent's text at all, mask the digits — masking and deleting
measured identically, so the marker is not load-bearing.

*Answers:*

```json
{ "verdict": "read" | "transform-invalid" | "wrong-region" | "needs-descent" | "illegible",
  "description": "...verbatim transcription...",
  "regions": [ ...only when verdict is needs-descent... ],
  "because": "one line, for a human reading the record" }
```

| verdict | what the child is claiming | what happens |
|---|---|---|
| `read` | this is the region, here are its characters | fills its placeholder; judged as now |
| `needs-descent` | legible, but finer detail is nested inside | recurse — its own rotation and filter per child |
| `refine` | the transform is wrong AND I can fix it from here | **self-transform, no parent** |
| `escalate` | the transform is fundamentally broken and I cannot fix it from in here | **TURN 3** |
| `illegible` | right box, right orientation, the pixels are not there | neither — re-render at higher dpi, or a human |

### REFINE versus ESCALATE — one question

**Escalate when the transform is fundamentally broken. Refine when you can fix it
without knowledge of the larger document.**

That is the whole test, and it is a question the child asks about ITSELF: *can I
specify the fix from what I can see?* Not "what kind of adjustment is this" — a
rotation can be either, depending on whether the child can tell which way is up
from its own crop or is guessing about the sheet.

| the child sees | can it specify the fix alone? | |
|---|---|---|
| a word truncated at its own edge | yes — ask for margin | **refine** |
| its own text is sideways | yes — rotate itself | **refine** |
| faint strokes, low contrast | yes — ask for a filter | **refine** |
| a reading about somewhere else entirely | no — the BOX came from outside | **escalate** |
| the whole frame is upside down, and it cannot tell from inside | no | **escalate** |
| nothing legible at any treatment | no fix exists | `illegible` |

Refine costs one call and no coordination. Escalate costs the parent's turn plus
a re-render — cheap now that turn 3 resumes turn 1's session, but it is still a
round trip, and it buys nothing when the parent has no information the child
lacks. "Give me half an inch more" is not a question about the document.

The corollary that matters for tiling: a seam cut is a REFINE, not an escalate.
Both neighbours see the same truncation from opposite sides and either can fix
it locally, and tiles ALREADY overlap by construction — so a tile widening its
own pad produces more of the duplication the design accepts, not a new problem.

### What this needs that does not exist

A child cannot currently ask to grow. Proposals are in the region's own 0..1
coordinates and `route` calls `clampToUnit`, so `{x:-0.05, w:1.1}` is trimmed to
exactly the region, `isDescent` then sees no shrink and routes it as a transform,
and the transform is built with `BBox: reg.BBox` — which discards the geometry a
second time. Growth is impossible to express and would be ignored if it were not.

The fix is not to loosen `clampToUnit`. Normalized coordinates are the wrong unit
here for the same reason `descentPadIn` is a length: half an inch has to mean the
same thing at every depth. It is a `margin` in INCHES on the proposal, applied
through the existing `paddedIn` helper, and a transform that takes the proposal's
rect instead of its parent's.

`transformHelped` already scores the result correctly with no change:
recovering `REPLAT OF BLOCK 40` from `REPLAT OF BLO` is more distinct content.

The computed `transform-suspect` flag stays alongside these. It catches the case
no verdict will: a child that confidently transcribes the wrong thing and
reports `read`.

---

**TURN 3 — re-orient.** The PARENT's turn, not the child's, because the child
cannot fix a choice it did not make.

*Given:* nothing new to look at. Turn 3 RESUMES TURN 1's SESSION and appends a
question. The page is already in that context, already at the root's resolution,
and — measured — already in the server's prefix cache:

```
turn 1   prompt 14,652   image + question     25.7s   cached      0
turn 2   prompt 14,734   text appended         1.1s   cached 14,648
turn 3   prompt 14,755   text appended         1.1s   cached 14,730
```

An escalation costs about a hundred tokens and a second, not another 14.6k and
25 seconds. The design requirement — that the escalation look at the ORIGINAL
page rather than the transformed crop, because re-asking over a suspect crop asks
the question inside the mistake — is satisfied by construction: the session's
image is the root's own untransformed render.

Appended as TEXT:

- the box and transform that were tried, and every other transform already tried
  on this region (the SHA set — a re-pick that renders to seen bytes is refused
  before it costs a call);
- the child's verdict and its `because`;
- the child's transcription, if it produced one — it may be a correct reading of
  the WRONG area, which is the evidence for a re-pick.

NOT appended: the child's crop as an image. That is a fresh encode and forfeits
the entire saving; the parent proposed the box and can reason about it from
coordinates.

*Two limits this imposes.* The parent is looking at the page at ROOT resolution —
four tokens per square inch on an E-size sheet — so it can re-pick and re-orient
and cannot check the child's characters. And turn 3 must never TRANSCRIBE: its
session holds the root's own low-resolution reading, so anything it wrote would
be the contaminated kind. `keep` means the child's reading stands, never "here is
a better one".

*Answers:*

```json
{ "action": "retransform" | "repick" | "keep" | "abandon",
  "regions": [ ...for retransform: same box, new rotation/filter...
               ...for repick: one or more new boxes, possibly nested... ],
  "because": "..." }
```

| action | meaning | next |
|---|---|---|
| `retransform` | box was right, rendering was wrong | turn 2 again, new render, budget spent |
| `repick` | box was wrong | turn 2 on the new boxes — nested transforms allowed |
| `keep` | the child was mistaken; the reading stands | placeholder filled, region flagged for a human |
| `abandon` | nothing here can be read | placeholder keeps the parent's fallback, flagged |

*Termination.* Turn 3 is bounded three ways, and all three already exist: the
image-SHA set refuses a re-pick that lands on pixels already read;
`MaxTransforms` bounds re-renders per region; `MaxCalls` bounds the document. An
escalation that produces neither new pixels nor a cleared flag ends the region as
a flagged leaf, which is the honest outcome.

*Cost.* Turn 3 is a call at the PARENT's resolution — on an E-size sheet the
most expensive call in the walk, and the one that buys the least per token. It
should be rare. If it is not rare, the sheet's region proposals are bad and
tiling is the cheaper answer.

---

### Sessions: append, never rewrite

Measured on this box, and it decides which turns may share a conversation.

**Reuse pays exactly when the IMAGE is unchanged.** Appending a turn to a
conversation reuses the image prefix — 25.7s to 1.1s, 14,730 of 14,755 tokens
served from cache. REWRITING the last message does not: the same image with a
different trailing question came back `cached=0` and paid the full 24.4s. So the
rule is literal — append a new turn, never edit the one before it.

| turn | same image? | session |
|---|---|---|
| 1, the sheet | — | opens it |
| 3, re-orient | YES, the same page | **resumes turn 1** |
| 2, a region | no, a different crop | **fresh, stateless** |

Turn 2 gains nothing from resuming: the child is looking at a crop, so there is
no shared prefix to exploit, and resuming would carry 14.6k tokens of page the
child does not need ON TOP of encoding the crop. A fresh session pays for the
crop alone. The efficiency argument and the contamination argument point the same
way for once — and batching siblings into one session fails both tests too, since
each has its own crop and region A's bearings would sit in context while region
B's are read.

There is a third reason turn 2 stays stateless, independent of both. The record
claims a human can be shown the image the words came from, and
`VerifyRegionRender` checks the crop against the recorded digest. If a
transcription came out of a session that also held the parent's page and its
readings, the text is not a function of that crop, and re-rendering plus
re-asking would not reproduce it. **A transcription turn must be stateless for
the record to mean what it says.** A decision turn carries no such burden —
nothing in the sidecar quotes it.

*Operationally.* This is a llama.cpp SLOT cache under `--parallel 1`. Any
competing request between turn 1 and turn 3 evicts it and the escalation pays
25.7s again — observed repeatedly while running these measurements. Treat the
saving as an optimisation, never as a budget assumption. And a retained session
grows: a page is ~14.7k of a 180k context, so roughly ten escalations on one
sheet before it matters, which is fine per page and needs eviction across pages.

### What each turn is allowed to decide

| | turn 1, sheet | turn 2, region | turn 3, re-orient |
|---|---|---|---|
| what is here | ✅ at its scale | ✅ at full resolution | — |
| where to look | ✅ proposes | ✅ proposes nested | ✅ re-picks |
| which way is up | ✅ proposes | ✅ **disputes** | ✅ decides |
| are the pixels damaged | ❌ measured | ❌ measured | ❌ measured |
| did a transform help | ❌ judged | ❌ judged | ❌ judged |
| is this region finished | ✅ `exhausted` | ✅ verdict | ✅ `keep`/`abandon` |

The model disputes a transform at turn 2 and decides one at turn 3, and never
decides whether the pixels are damaged — measured, it calls recoverable blur
"skew" at 0.9 confidence and prescribes the repair that makes it worse.

### What is deliberately not in the protocol

- **The reader is never asked to confirm a measurement.** It would agree.
- **A repair is never applied because a flag fired.** The flag is told to the
  reader; the reader asks or does not; the judge keeps it or throws it away. Three
  parties have to agree before a filter survives into the record.
- **No party may apply two repairs at once.** Measured: contrast alone recovers a
  fact and mild sharpening alone recovers it, and the two stacked recover nothing.

## Descent versus transform — and the cycle it creates

Observed: the model will propose, as a "sub-region", the region it was just
given — re-rotated, or thresholded, or simply asked for again. Sometimes that is
the right instruction. A faint scan genuinely reads better binarized; a block
whose text runs at 30° genuinely needs re-rotating. But as a CHILD it is not a
descent, and treating it as one recurses forever on the same pixels.

So they are two different operations and must be counted separately.

**Descent** narrows the field of view: a child bbox materially smaller than its
parent. Bounded by depth and by a minimum region size.

**Transform** re-renders the SAME region differently — rotation, threshold,
contrast, dpi. Bounded by its own per-region budget, and subject to two rules
that descent does not need.

### A transform must be new

Cheap and exact, because the page cache already keys on the SHA-256 of the image
bytes: **if a proposed transform renders to bytes already seen in this
document's descent, it is a cycle — refuse it.** Rotating 90° four times returns
the original SHA. Re-asking for the same bbox at the same dpi and rotation
returns the original SHA. The cache that makes retries cheap is also the cycle
detector, at no extra cost.

### A transform must make progress

A transform is justified by a FLAG it is meant to clear — `low-resolution` wants
more dpi, `repetition` wants a narrower field or a different rotation,
`disagreement` wants a threshold. If the result carries the same flags as the
input, the transform bought nothing and the branch stops. Two transforms that
each fail to clear a flag end the region: it is a leaf, flagged, and a human
reads it.

### The boundary between them

A child whose bbox covers most of its parent is a transform wearing a descent's
clothes. Route by geometry, not by what the model called it: below some fraction
of the parent's area it is a descent and spends depth; at or above it, it is a
transform and spends transform budget. Anything the model proposes that is
neither smaller nor differently rendered is refused outright.

## Confidence: flags, not a number

A score here would be an invented statistic. What is actually computable:

- **`low-resolution`** — tokens per square inch for this region against the
  letter-page baseline of ~39. Pure arithmetic, available BEFORE the model call,
  and it alone would have condemned the survey sidecar the moment it was
  written.
- **`repetition`** — agentkit already detects degenerate loops and reports the
  block and count. On a whole page it fails the document; on a region it should
  mark the region and force a descent.
- **`clipped`** — ink touching the region boundary, so text may be cut. Cheap to
  measure and it is exactly what the verification tile hit.
- **`disagreement`** — the cheap engine (tesseract) and the VLM produce
  materially different text. raglit already runs both and has a gibberish gate.
- **`cycled`** — a proposed transform rendered to bytes already seen, or two
  transforms in a row cleared no flag. Records that descent stopped because it
  stopped paying, which is different from stopping because it finished.
- **`exhausted`** — the model proposed no further regions. The one positive
  signal, and the only one that means "this is as good as it gets".

A region carrying `low-resolution` or `repetition` is a descent trigger. A leaf
that still carries them after descent is a region a HUMAN should look at, which
is the honest end state for a six-point bearing on a scanned plat.

## Showing a human the image the words came from

The consuming system asks a person to ATTEST that a document really contains the
words a fact quotes. It hands them the page. That is the wrong artifact as soon
as the machine did not read a page: text transcribed from a rotated, zoomed crop
was read at a resolution the whole sheet never had, and the whole sheet is where
the legal description vanished in the first place. A person checking against it
is checking a different image than the one that produced the text.

So the tree is durable and every region is addressable.

- **`<doc>.raglit-regions.json`**, beside the document, the same convention as
  the transcription. JSON, not markdown: a tree of geometry is data, and a
  drawing has no reading order to render as prose — which is the reason regions
  exist at all.
- **`p1`, `p1.0`, `p1.0.2`** — page, then the path down. Readable in a diff, and
  the path IS the ancestry, so a hit reports as *sheet → drawing interior → lot C
  corner* without a lookup. It addresses a position in a RECORDED tree, not a
  piece of paper: a second read proposes different regions and renumbers. The
  digest is what says whether an id still points at the pixels the text came from.
- **The digest was already being computed.** It is the descent's cycle detector.
  Keeping it rather than discarding it is the whole of what turns "same
  coordinates" into "same image".
- **Attribution is spans in the sidecar, never markers in the markdown.**
  `## Page N` is a contract two separate consumers parse, and anything
  interleaved with the text lands inside the quotations they match against.
- **The transcript keeps interior overviews, not only leaves.** Dropping them
  would undo the coverage guarantee above. It duplicates, and that is the honest
  artifact — an overview read at four tokens per square inch is exactly where
  invented text lives, and the spans are what let a reader see that a sentence
  came from a low-resolution region and ask for that crop.

### What is reproducible, and what is not

- **The crop is.** `pdftoppm -png -r <dpi>` is byte-identical run to run at a
  fixed version (measured), and the crop-plus-rotation off it is pure Go with no
  resampling. A re-render hashes to the recorded digest. Across renderer
  VERSIONS there is no such guarantee, so the read also records the page's pixel
  dimensions: a rasterization that comes out a different size at the same nominal
  dpi is reported as that, rather than discovered downstream as an unexplained
  digest mismatch.
- **The image the MODEL saw may not be.** A page too large for the context is
  re-rendered smaller mid-call (`maxContextShrinks`). That is recorded per region
  as a count and replayed by `RerenderRegionAsSeen`, deterministically. It is
  deliberately not what the digest covers: the digest is taken BEFORE the call,
  because it is also the cycle detector and a cycle has to be caught before the
  call is paid for.
- **The crop is the attestation image**; the as-seen image answers the different
  question of whether the model could have read it at all. Where they differ the
  crop is SHARPER — more detail on the same pixels, never different pixels.

## What this costs

More requests per sheet, and an unbounded shape if the model is generous with
regions. Bound it: max depth, max regions per node, a per-document request
budget, and a minimum region size below which descent cannot help.

Against that: today a large sheet costs one request and produces something worse
than nothing, because it reads as complete.

## What a transform is worth — measured 2026-08-03

Everything above was reasoned from one page and one recovered clause. This is
the same argument with numbers on it, and two of them contradict what is written
above.

Setup: `bench/probes/ocr-survey-corners/_fixture/page.png` — page 2 of the
survey, 3400 x 4400 at 400 dpi, 15.0 MP — read by `Qwen3-6-27B-MPT` through
corrallm, with the probe's own prompt and nothing else changed. Ground truth for
the EXISTING CORNERS table was established by reading the crop by eye. Five facts
discriminate, because they are the ones a reader gets wrong in a way that still
reads correctly:

| | truth |
|---|---|
| J's bearing | `S 31°05' E 0.4' FROM CALC` |
| K's bearing | `S 47°44'E 2.0' FROM CORNER` |
| L's offsets | `0.1'S AND 0.1'W OF CALC` |
| the cap stamp | `MOWRER` (not MOWER) |
| S's reference | `LISSER 20123169` |

### Rotation is the larger lever, and the cheaper one

| variant | J | K | L | MOWRER | S | output |
|---|---|---|---|---|---|---|
| whole sheet, sideways, 15.0 MP | `5.31'` | `5.47'` | `0.15'/0.14'` | `MOWER` | `L1SSER` | 9,316 ch |
| whole sheet, UPRIGHT, 15.0 MP | ✓ | `S 41°44'E` | ✓ | ✓ | ✓ | 2,187 ch |
| drawing region, sideways, 9.2 MP | ✓ | ✓ | ✓ | `MOMER` | `2012369` | 13,715 ch |
| drawing region, UPRIGHT, 9.2 MP | ✓ | ✓ | ✓ | ✓ | ✓ | 2,087 ch |

**Rotation alone fixes four of five. Cropping alone fixes three of five. Together
they fix all five.** They are complementary — each cleans up what the other
leaves — but rotation is a transpose and cropping is another model call, so the
cheaper operation is also the bigger one. Rotate before descending.

Note what sideways does to the OUTPUT, not just its accuracy: 4-7x the
characters for the same content, and the 9.2 MP sideways read ran into the token
ceiling. Sideways text is how the model loses count. That is the same mechanism
as the repetition guard firing 23 times on `FND. R/C MOWRER (6/12/2002)`, which
was read at the wrong orientation on a page nobody had rotated.

### `transformHelped` prefers the wrong render

The consequence of the line above, and a live bug. `transformHelped` accepts a
transform that clears a flag, else one that produced MORE text. On this page the
correct read is the SHORTER one, every time — 2,187 against 9,316 on the sheet,
2,087 against 13,715 on the region. Unless the repetition guard fires (the
sideways sheet's most-repeated line recurred 3 times, likely under threshold),
the length rule rejects the rotation that recovers the bearings.

The comment on that function is right that flags alone rejected every rotation.
Length is the wrong substitute. What actually separated these four runs is
AGREEMENT — J, K, L, MOWRER and the certificate number are identical across the
two good renders and differ in the two bad ones — which is the `disagreement`
signal already named under Confidence, applied between two renders of one region
rather than between two engines.

Measured on raw model output through a scratch client, not through
`PageAsSeen`; whether the guard fires inside raglit's path on these two is
untested.

### Rotation can be MEASURED, which settles half an open question

`tesseract --psm 0 --tessdata-dir <dir>` on the same images:

| image | reports | confidence |
|---|---|---|
| the whole sheet | `Orientation 90°, Rotate: 270` | 2.28 |
| the drawing region, sideways | `Orientation 90°, Rotate: 270` | 1.91 |
| the corners table, already upright | `Orientation 0°, Rotate: 0` | 0.10 |

Correct all three, about a second each, no model call. It needs
`osd.traineddata` (10 MB) which is NOT installed — and this tesseract build
ignores `TESSDATA_PREFIX`, so it wants `--tessdata-dir`. It also called the
sheet's script Japanese at confidence 0.27, so gate on the number.

The suspicion above — that OSD "says nothing useful for a drawing interior" — did
not reproduce. It read the drawing region correctly. What it cannot report is
anything other than a right angle, which is the next finding.

### Skew does not damage a reading, and the render geometry is a lottery

`rotateImage` refuses arbitrary angles because "an arbitrary angle needs
resampling and would blur the small lettering this exists to make readable, and
a text block on a scanned sheet is square to the page far more often than not."

The second half holds for this corpus. Projection-profile estimates over all four
bench fixtures — including the faxed permit drawing and a page of the scanned
30-page instrument — come out at `+0.25°`, `+0.25°`, `0.00°` and `-0.50°`.

The first half is wrong, and testing it properly turned up something else.
Skewing the corners table 2° cost nothing, but that table is legible at 400 dpi
and proves little. The page that is AT the resolution limit is
`ocr-survey-facts` — 3.6pt lettering, 10 px of glyph height — and skewing that
one does not degrade it either. It improves it:

| `ocr-survey-facts`, Qwen3-6-27B-MPT, temp 0 | checks | missed |
|---|---|---|
| square | 5/7 | `202107080106`, `LISSER` |
| square, run again | 5/7 | the same two — deterministic |
| skewed 1.15° (2% slope) | 6/7 | `LISSER` |
| skewed 2° | 7/7 | — |
| rescaled 1.05x, NOT rotated | 6/7 | `20123169` |

The control is the interesting row. A plain rescale with no rotation also beats
the square baseline — and recovers a DIFFERENT fact while losing one the baseline
had. So the lever is not skew, and not the sharpening a resample incidentally
does. It is that a page at the resolution limit lands on the vision encoder's
patch grid one particular way, and small glyphs fall on the wrong side of that
grid or the right side of it depending on geometry nobody is choosing
deliberately.

Which means **there is no single correct render of a marginal page, and one render
is a sample**. `202107080106` was recorded in plan/ocr-fixtures.md as recoverable
only via the digit-stripped tesseract assist; it is recoverable by rotating the
page 2°, which is not a fact about tesseract or about the assist.

Consequences, in order of confidence:

- **Skew correction is not urgent and skew tolerance is not the risk it was
  assumed to be.** Right angles remain the only rotation the descent applies.
- **Two renders of one region disagreeing about a number is now an expected
  outcome**, not a malfunction. It is the `disagreement` signal, available from
  one model and one page by perturbing geometry — no second engine required.
- **Consensus over perturbed renders is the obvious next experiment** for pages
  carrying `low-resolution`: read at 2-3 geometries, keep what agrees, flag what
  does not. NOT built, and one run per geometry is far too thin to size the win.

The caution that belongs with this: five readings, one page, one model. What is
solid is that the baseline is deterministic and the perturbed renders differ from
it in specific, named facts. What is not established is which perturbation to
prefer, or that any of this generalises past this sheet.

### Filters repair damage; they do not improve a good render

The first pass at this concluded that no filter helped. That test ran six filters
against a region already scoring 5/5, where the only available outcomes were "no
change" and "worse" — it could not have shown a gain, and the conclusion drawn
from it was not supported. What follows replaces it.

**On a real page with headroom.** `ocr-survey-facts` sits at 2 of the 4 facts
plan/ocr-fixtures.md records, reproducibly:

| 400 dpi, unmodified page | cert | note2 | deed | LISSER | |
|---|---|---|---|---|---|
| no filter | ✓ | ✗ | ✓ | ✗ | 2/4 |
| **CLAHE clip 2.0, 8x8** | ✓ | **✓** | ✓ | ✗ | **3/4** |
| CLAHE clip 4.0, 16x16 | ✓ | ✗ | ✓ | ✗ | 2/4 |
| autocontrast (min-max) | ✓ | ✗ | ✓ | ✗ | 2/4 |
| **unsharp mild (σ1.0, α0.4)** | ✓ | **✓** | ✓ | ✗ | **3/4** |
| unsharp (σ2.0, α0.8) | ✓ | ✗ | ✓ | ✗ | 2/4 |
| Sauvola (w25, k0.2) | ✓ | ✗ | ✓ | ✗ | 2/4 |
| **Otsu** | ✓ | **✓** | ✓ | ✗ | **3/4** |
| median 3x3 + unsharp | ✓ | ✗ | ✓ | ✗ | 2/4 |
| CLAHE + unsharp | ✓ | ✗ | ✓ | ✗ | 2/4 |

Local contrast recovers `202107080106`, which plan/ocr-fixtures.md records as
needing the digit-stripped tesseract assist. Replicated across two runs.

Three things in that table, and two of them are warnings.

**The mild setting of a filter helps and the aggressive setting of the same
filter does not** — CLAHE at clip 2.0 but not 4.0, unsharp at α0.4 but not α0.8,
a global stretch not at all. A filter that helps is a TUNED filter, which is the
argument for shipping few of them, each measured, rather than a configurable
chain.

**Stacking two filters that each work recovers nothing.** CLAHE alone 3/4, mild
unsharp alone 3/4, CLAHE+unsharp 2/4. Whatever each one is doing, the composition
undoes it. One repair at a time.

**Otsu reached 3/4 here** — and on the corners region the same filter was the
worst thing tried, losing a bearing and running 4.6x long into the token ceiling.
Both are clean 400 dpi renders of the same document. So global binarization is
not "bad for a VLM" as an earlier version of this section claimed; it is
UNPREDICTABLE, which is worse for a pipeline and is why it is not implemented.

Nothing recovers the surveyor's name. Only geometry has ever moved that one.

**On known damage.** The corners table reads 5/5 clean; degrading it
deliberately gives every correction something real to undo:

| damage | none | deskew | deconv | CLAHE | deskew+deconv | deskew+CLAHE+deconv |
|---|---|---|---|---|---|---|
| 4°, blur σ1.6 | 4/5 | **5/5** | **5/5** | **5/5** | **5/5** | **5/5** |
| 4°, blur σ2.6 | 3/5 | **1/5** | 3/5 | 3/5 | 3/5 | 3/5 |
| 6°, blur σ3.4 | 0/5 | 1/5 | 1/5 | 0/5 | 1/5 | — |
| 4°, σ2.6, faded | 0/5 | 1/5 | **3/5** | **3/5** | 1/5 | **3/5** |

Three things fall out of that grid.

**The recoverable window is narrow.** At mild blur every correction restores the
page completely. One step further and nothing restores the two lost facts — the
information is gone, and no amount of sharpening invents it back. A filter is
worth trying and is not worth trusting.

**Deskew ALONE is harmful in the blur band** — 3/5 down to 1/5, worse than
leaving the damage alone, because rotating already-blurred pixels resamples them
a second time. That is precisely `rotateImage`'s stated reason for refusing
arbitrary angles, which the skew section above reports as unsupported. Both are
right: on a SHARP image the resampling costs nothing, on a blurred one it
compounds. The comment describes a real regime; it just is not this corpus's.

**Fade is the case with the most to gain.** A faded scan reads NOTHING unfiltered
and three of five with contrast or sharpening. This is the one damage mode where
a filter is the difference between a page and no page.

Binarization is the one that refused to generalise: worst-of-all on one clean
render, joint-best on another. Not implemented for that reason — see the table
above.

Upscaling remains pointless past `maxImageTokens` — the encoder downsamples it
straight back and the resampling is pure loss. `tokensForImage` already knows.

### Who asks for the transform

The region prompt already asks the model for a `rotation` per proposal, so
extending it to filters costs nothing structurally. Whether it SHOULD is a
question about what the model can see, and it splits:

| shown | model said | model's fix | its confidence | measured lapvar |
|---|---|---|---|---|
| clean crop | nothing | none | 1.0 | 6593 |
| blur σ1.6 | skew, noise | deskew, denoise | 0.9 | 53 |
| blur σ3.4 | blur, low contrast | sharpen, contrast, rescan | 0.95 | 2.0 |
| skew 4° | skew | deskew | 0.95 | 4601 |
| skew 4° + blur σ2.6 | blur, skew | deskew, sharpen | 0.9 | 5.6 |
| sideways | skew | **rotate270** | 1.0 | 6593 |
| whole sheet at root scale | skew, low contrast | **rotate270**, contrast | 0.95 | 4283 |
| root scale + blur σ3 | blur, skew, contrast, too small | rotate, sharpen, rescan | 0.95 | 1.5 |

**Rotation: let the model ask.** It named `rotate270` correctly on the sideways
crop AND on the whole sheet seen at root scale. The worry that a root at 4 tokens
per square inch cannot judge its own image did not reproduce — it read the root
view correctly and did not falsely report blur.

**Blur and contrast: measure, do not ask.** At σ1.6 — the ONLY damage level where
a filter fully recovers the page — the model diagnosed `skew` and `noise` and
prescribed `deskew`, which is the one correction measured to make a blurred
region worse. It was 0.9 confident. Confidence sits at 0.9-1.0 whether it is
right or wrong, so it cannot be used to gate the decision.

The variance of the Laplacian catches every case the model missed, on the
FULL-RESOLUTION crop — pixels the model never receives, because the encoder
downsampled them before it ever saw the region:

| image | lapvar | dynamic range |
|---|---|---|
| clean crop | 6593 | 255 |
| damaged 4°/σ1.6 | 51 | 118 |
| fixture ocr-survey-facts | 5135 | 255 |
| fixture ocr-survey-corners | 2967 | 255 |
| fixture ocr-scanned-exhibit | 2562 | 243 |
| fixture ocr-drawing-dimensions (a fax) | 844 | 232 |

Two orders of magnitude between damaged and clean, every real fixture well clear
of any threshold in between, and the faxed sheet correctly the lowest of them.
Milliseconds, no model call — the same shape as `TokensPerSqIn`, which is already
computed before the model is asked anything.

So: **the model proposes rotation, a measurement proposes filters**, and both
arrive as transforms that `transformHelped` judges the same way. That is not an
architectural preference; it is where each one was measured to be reliable.

Not built. What it needs is a `blurred`/`faded` flag computed alongside
`low-resolution`, a filter on the transform record for the descent to apply, and
the discipline that a filter transform must clear the flag that justified it —
which the existing machinery already enforces.

### The 1.2B document parser these measurements came out of

MinerU2.5-Pro-1.2B (`opendatalab/MinerU2.5-Pro-2605-1.2B`, a Qwen2-VL derivative)
was evaluated as a cheaper reader and as a region proposer. It is SOTA on
OmniDocBench v1.6 — 95.72 overall against Qwen3-VL-235B's 89.78 — and none of
that transferred here.

- **As a reader** it matched the 27B on content at equal field of view and lost on
  structure: it renders the table's circled index letters as circled NUMERALS
  (`B`→`⑧`, `J`→`⑦`, `S`→`⑤`) and `0.1'` as `O.I'`. Right descriptions, wrong keys.
- **As a region proposer it cannot subdivide a drawing.** Its layout pass returns
  the sheet's whole interior as one `image` block; one level in, another `image`
  block of nearly the same extent; a level below that, exactly one block — itself.
  Forced to read the leaf over its 1.6 MP input cap it degenerated into a loop,
  386 seconds emitting `A = FROEN 1/2 BACPA 5,6491` through Z and wrapping. The
  same failure this document exists to prevent, from a different model.
- **It does not fit.** 2.15 GiB of weights, ~2.8-3.0 GiB resident, against 366 MiB
  free on a box where the 27B is the resident model. Buying it means ~110-130k of
  that model's context.

Kept as a research point. The one genuinely useful thing it produced was the
top-level figure bbox with its rotation — one box, which the 27B can propose
itself, and which this document already specifies asking for.

## Open decisions

- **Do leaves replace fragments, or feed them?** Leaves-as-fragments is honest
  for a drawing and changes what a search hit means. Stitching leaves back into
  prose keeps behaviour uniform and re-invents the reading order a drawing does
  not have. `RegionTranscript` assembles a page's text and deliberately decides
  nothing here — it is a transcription, not a fragmentation.
- **Who decides rotation?** SETTLED for right angles — tesseract OSD measures it,
  including on the drawing interior it was expected to fail on. Open: the
  `osd.traineddata` dependency is not installed, and the confidence threshold
  below which the model should be asked instead is unchosen (0.10 on an upright
  page against 1.91-2.28 on sideways ones is the only spread measured).
- **Trigger.** Physical size alone (> ~1.2 letter pages) is a crude but
  sufficient gate, and it needs no model call to evaluate. Nothing evaluates it
  yet: the descent runs only when `raglit regions` is invoked, so a large sheet
  ingested normally is still read whole.
- **Does the transcription sidecar carry the region-assembled text?** Today
  `--write` records the assembled text in the REGIONS sidecar, and ingest still
  writes `## Page N` from the ordinary per-page read. Merging them means deciding
  the fragments question above first.

## Settled

- **Rotation earns its budget.** It alone recovers four of five discriminating
  facts on a sideways sheet, for the cost of a transpose. Measured 2026-08-03,
  below.
- **Filters repair damage; they do not improve a good render.** Local contrast
  recovers a fact on a real page and rescues a faded scan from reading nothing.
  On an undamaged render the same filters are neutral and binarization hurts, and
  past a blur threshold nothing recovers anything. Gate them on measured damage.
  An earlier entry here claimed filters were worthless; that was concluded from a
  region already scoring full marks, where no gain was possible.
- **The model asks for rotation; a measurement asks for filters.** It names
  `rotate270` correctly even at root scale, and misdiagnoses recoverable blur as
  skew at 0.9 confidence — prescribing the one correction that makes a blurred
  region worse.
- **A transform is judged by agreement, not by length.** The correct render is
  consistently the SHORTER one, because the wrong orientation makes the model run
  on rather than stop. Fixed 2026-08-03: `transformHelped` now refuses a render
  that mostly repeats itself, then prefers the one that accounts for what the
  PARENT said is here, and falls back to distinct rather than raw length.
- **A child that shares nothing with its parent's account indicts the TRANSFORM,
  not the page.** A rotation applied the wrong way round comes back describing
  somewhere else. Recorded as `transform-suspect`; acting on it means re-rendering
  the parent, and that loop is deliberately not built.
- **Is it worth building here?** Yes, and the reason turned out not to be the
  transcription quality. It is that a person cannot attest a quotation against an
  image the quotation did not come from, and without the region record there is
  no way to produce that image at all. Marking a plat READ BY EYE still applies;
  it is now a check somebody can actually perform.
