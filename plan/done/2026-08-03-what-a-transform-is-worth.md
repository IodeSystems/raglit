# 2026-08-03 — what a transform is worth, measured

One entry of `plan/done/`: completed work moved out of the active plans so those
stay readable in one sitting. A file per finished thing, because a single archive
balloons until nobody opens it. Says what was settled and where the detail lives.

Four fixtures, six configurations, one harness, plus the first execution of the
region descent against the sheet it was designed for. Detail in
`plan/ocr-fixtures.md` (the numbers) and `plan/hierarchical-regions.md` (what
they changed about the design).

**Settled:**

- **Tiling recovers a sheet nothing else reads.** The 27x36.7in survey: 4/4 facts
  plus both refusals tiled, against 1/4 untiled with the same model, guards and
  dpi. It returns `9308270057` where every whole-sheet read confabulates
  `A#200308270057`.
- **The repetition guard is load-bearing and cannot run non-streaming.** Measured
  bypassing it: a 29-byte block repeated 328 times. Through raglit the same sheet
  duplicates 0.00. Any measurement taken with a plain POST is measuring an
  unprotected model, not raglit.
- **Assist wins the single-pass comparison** — 14 against 12-13 for anything
  geometric, and stacking rotation on assist adds nothing.
- **The geometry lever is RESAMPLING, not rotation.** The +-2 degree result
  recorded earlier was an expanding canvas doing the work; a plain resize to the
  same dimensions with no rotation scores identically. Target size matters and is
  unexplained: 3552x4516 gives 5/5, 3570x4620 gives 4/5.
- **descentPadIn was reasoned from line height against a width problem.** Half the
  words on that survey are wider than the old 0.15in pad. 0.5in is the largest
  that still lands on the token cap, and takes seam cuts from 22 to 3.
- **Three of four original fixtures are saturated.** `plain` scores full marks on
  the drawing, the corners table and the exhibit, and their substring checks pass
  on readings verified wrong by eye. A substring check cannot measure a
  transcription.
- **MinerU2.5-Pro-1.2B is a research point, not a plan.** SOTA on OmniDocBench,
  matches the 27B at equal field of view, cannot subdivide a drawing, and does not
  fit in the VRAM left beside the resident model.
- **Old region records need no migration.** 39 of 39 re-render to their recorded
  digests under the new code; `--backfill-damage` measured them without a model
  call.

**Shipped:** the filter/damage machinery, `transformHelped` judged by agreement
rather than length, `transform-suspect`, self-refinement via `margin`, the grid
rule for tiles, `--backfill-damage`, and two dead bench checks fixed (`HALVOR`,
`Brannock`) plus a `make-fixtures.sh` that pointed at a nonexistent file.

**NOT done, and still active in `plan/hierarchical-regions.md`:** the turn-3
escalation loop, placeholder assembly, and wiring any of it into ingest. Nothing
here changes how a document ingested today is read.
