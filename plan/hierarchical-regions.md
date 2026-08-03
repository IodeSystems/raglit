# Hierarchical regions — reading an image that does not fit in one look

Status: **built** (`regions.go`, `regionread.go`, `regiondoc.go`, `raglit
regions`, `raglit region`). Not yet wired into ingest — the descent runs only
when asked for. Opened 2026-07-29 after a recorded land survey produced a
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
- **`filters[]`** — no filter improved a reading and two degraded one. Where
  filtering belongs is on the ENGINE, and tesseract is the tier that wants it.

Leaves are what get embedded and searched. Interior nodes are searchable too,
carrying their overview text. `bbox` in PAGE coordinates so any node can be
re-rendered from the original without replaying the descent.

The existing per-page image cache already keys on the SHA of the image bytes, so
every region — being an image — is cached by construction and a re-run costs
nothing for regions that have not changed.

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

### Filters: nothing helped, and binarization hurt

Same region, upright, one filter each:

| filter | J | K | L | MOWRER | S | output | wall |
|---|---|---|---|---|---|---|---|
| none | ✓ | ✓ | ✓ | ✓ | ✓ | 2,087 ch | 21.3s |
| unsharp mask (σ2, α1.8) | ✓ | ✓ | ✓ | ✓ | ✓ | 3,601 ch | 32.5s |
| CLAHE (clip 2, 8x8) | ✓ | ✓ | ✓ | ✓ | ✓ | 2,048 ch | 21.9s |
| Otsu | ✓ | ✗ | ✓ | ✓ | ✓ | 13,427 ch | 98.6s |
| Sauvola (w25, k0.2) | ✓ | ✓ | ✓ | ✓ | ✓ | 2,671 ch | 25.2s |
| median 3x3 + unsharp | ✓ | ✗ | ✓ | ✓ | ✓ | 3,575 ch | 33.1s |

No filter recovered anything the unfiltered region did not already have. Two lost
K's bearing. Global binarization was the worst of them: 4.6x the wall clock and
straight into the token ceiling.

Which contradicts "a faint scan genuinely reads better binarized" as written
under Descent versus transform. The reason it is wrong is structural, not a
tuning miss — that whole preprocessing stack was built for binarization-based
engines, and a vision encoder reads grayscale antialiasing as signal that
thresholding throws away.

Two consequences:

- **A `threshold` transform has no evidence behind it.** Do not ship one until a
  page exists that it rescues. This region is a clean 400 dpi render, not a faint
  scan, so the faint-scan case is genuinely untested — `ocr-scanned-exhibit` is
  where to test it.
- **Filters belong on the ENGINE, not the region.** Tesseract is the tier that
  wants Sauvola and a 2x upscale, and `ocrengine.go` already computes
  `MedianGlyphPx`, which is exactly the trigger for deciding it. The VLM wants
  the pixels. That is a `PageEngine` concern and does not belong in the region
  record.

Upscaling deserves its own note: past `maxImageTokens` it is worse than useless,
because the encoder downsamples it straight back and the resampling is pure loss.
`tokensForImage` already knows this.

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

- **Rotation earns its budget; filters and skew do not.** Rotation alone recovers
  four of five discriminating facts on a sideways sheet, for the cost of a
  transpose. Six filters recovered nothing and two lost a bearing. Skew is
  absent from every fixture and harmless when introduced. Measured 2026-08-03,
  below.
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
