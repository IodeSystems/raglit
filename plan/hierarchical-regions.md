# Hierarchical regions — reading an image that does not fit in one look

Status: **design, not built.** Opened 2026-07-29 after a recorded land survey
produced a confident, complete-looking transcription that had silently dropped
the clause the matter turns on.

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
- **`exhausted`** — the model proposed no further regions. The one positive
  signal, and the only one that means "this is as good as it gets".

A region carrying `low-resolution` or `repetition` is a descent trigger. A leaf
that still carries them after descent is a region a HUMAN should look at, which
is the honest end state for a six-point bearing on a scanned plat.

## What this costs

More requests per sheet, and an unbounded shape if the model is generous with
regions. Bound it: max depth, max regions per node, a per-document request
budget, and a minimum region size below which descent cannot help.

Against that: today a large sheet costs one request and produces something worse
than nothing, because it reads as complete.

## Open decisions

- **Is it worth building here?** raglit's corpora hold a handful of large-format
  plats. Marking them READ BY EYE — which the evidence rules already require for
  anything a filing quotes — may be cheaper than this subsystem. That is a
  judgement about the corpus, not about the design.
- **Do leaves replace fragments, or feed them?** Leaves-as-fragments is honest
  for a drawing and changes what a search hit means. Stitching leaves back into
  prose keeps behaviour uniform and re-invents the reading order a drawing does
  not have.
- **Who decides rotation?** The model can propose it per region; tesseract OSD
  can measure it cheaply for text-dominated regions and says nothing useful for
  a drawing interior.
- **Trigger.** Physical size alone (> ~1.2 letter pages) is a crude but
  sufficient gate, and it needs no model call to evaluate.
