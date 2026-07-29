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
  page, bbox_x, bbox_y, bbox_w, bbox_h,   -- in page coordinates, not tile
  rotation,                                -- applied before OCR
  scale_dpi,
  kind,                                    -- overview | text-block | table | drawing | legend
  text,                                    -- transcription or description
  flags,                                   -- see below
  depth
)
```

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

## Open decisions

- **Do leaves replace fragments, or feed them?** Leaves-as-fragments is honest
  for a drawing and changes what a search hit means. Stitching leaves back into
  prose keeps behaviour uniform and re-invents the reading order a drawing does
  not have. `RegionTranscript` assembles a page's text and deliberately decides
  nothing here — it is a transcription, not a fragmentation.
- **Who decides rotation?** The model can propose it per region; tesseract OSD
  can measure it cheaply for text-dominated regions and says nothing useful for
  a drawing interior.
- **Trigger.** Physical size alone (> ~1.2 letter pages) is a crude but
  sufficient gate, and it needs no model call to evaluate. Nothing evaluates it
  yet: the descent runs only when `raglit regions` is invoked, so a large sheet
  ingested normally is still read whole.
- **Does the transcription sidecar carry the region-assembled text?** Today
  `--write` records the assembled text in the REGIONS sidecar, and ingest still
  writes `## Page N` from the ordinary per-page read. Merging them means deciding
  the fragments question above first.

## Settled

- **Is it worth building here?** Yes, and the reason turned out not to be the
  transcription quality. It is that a person cannot attest a quotation against an
  image the quotation did not come from, and without the region record there is
  no way to produce that image at all. Marking a plat READ BY EYE still applies;
  it is now a check somebody can actually perform.
