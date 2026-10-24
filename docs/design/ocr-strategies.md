# OCR read strategies — declared, selected, and (later) detected

How much work a page is worth is a policy, and it used to live in three places
with three different lifetimes: a few knobs in `config.json`, the region
descent's budgets as `raglit regions` FLAGS that vanished when the command
exited, and the automatic resolution rule as four package constants that needed
a rebuild to change. A policy therefore could not be attached to a corpus.

## Shipped (2e0d0d4)

`ocr.strategies` is a map of named bundles; `indexes.<name>.ocr_strategy` picks
one; `--strategy` on `raglit ocr` forces one.

```json
"ocr": {
  "strategy": "flat",
  "strategies": {
    "flat":   { "descend": 0 },
    "survey": { "descend": 2, "tile": true, "max_calls": 40,
                "hint": "every bearing, distance and monument call",
                "render": { "target_glyph_px": 24, "max_dpi": 400 } }
  }
},
"indexes": { "records": { "roots": ["records/"], "ocr_strategy": "survey" } }
```

**Precedence:** `--strategy` > (detected, unbuilt) > index's `ocr_strategy` >
`ocr.strategy` > zero value.

Two rules the implementation holds to, both testable and tested:

- **Every field is zero-valued to today's behavior.** This governs model spend.
  A config format whose defaults are not the current behavior turns an upgrade
  into a bill.
- **An unknown strategy name degrades to the ZERO value, not to the project
  default.** A typo must not stop an ingest, and inheriting the default would
  hide it behind output that looks reasonable. `--strategy` additionally prints
  a warning, because a name typed on the command line was meant.

`RenderPolicy` is carried on `OCR` (not plumbed separately) because `OCR` is
already threaded to every extract path; a policy needing its own plumbing is one
that reaches some paths and not others.

## Not shipped

- **`ocr_strategy` does not bite on ingest.** The pipeline builds one `OCR` per
  command and does not know which index a document belongs to, so it takes the
  project default's render policy. Threading the index through
  `pdfUnits`/`ingestPDF` is the remaining work.
- **Detection.** Nothing measures a page and picks a strategy for it.
- **Auto-descend.** `StrategyConfig.AutoDescend` exists as a field and nothing
  reads it. The intended gate is the low-resolution test the descent already
  computes — 14 of 998 pages in this corpus (1.4%), in 11 of 154 documents.

## Detection: measure, do not ask

**A model-detected `kind` gate was already tried here and removed.** Do not
reintroduce it without reading this.

Tiling used to require `kind == "drawing"`, where `kind` came from the model
looking at the region. Measured 2026-08-03 over every oversize page in the
corpus:

| | |
|---|---|
| flagged low-resolution by arithmetic | 13 of 13, right every time |
| called `drawing` by the model | **1 of 13** |
| called `overview` | 11 (including four 27x36in recorded surveys) |
| called `text-block` | 2 |

The gate suppressed tiling on exactly the pages that needed it. The cause is
structural, not a prompt defect:

> A REGION CANNOT BE PROPOSED BY SOMETHING THAT CANNOT SEE IT.

At 4 tokens per square inch against a readable baseline of 39, block capitals in
a title block survive the downscale and 6pt bearing labels do not. `overview` is
a compliant answer from the prompt's own list. No budget fixes it: 40 calls gave
23 regions and zero geometry; 200 gave 28 and zero geometry.

So the split for any future detector:

| approach | reliable at low resolution | cost |
|---|---|---|
| **measure** — page inches, token density, glyph height, text-layer presence, ink coverage, column projection profile, morphological ruling-line detection | yes | free to ~1s |
| **ask the model** — "is this a table?" | no | a call |

Classical CV covers most of the multi-component cases without a model and
without the blindness problem: a projection profile finds columns, ruling-line
detection finds tables, ink-coverage density separates drawing from prose. Those
are measurements, so they are trustworthy at exactly the resolutions where the
model is not.

A model-supplied `kind` is still worth having **at depth**, where the region is
resolved enough for the label to mean something. Never at a blind root.

## The budget rule

**Detection chooses WHICH policy applies. Config owns HOW MUCH gets spent.**

Budgets (`max_calls`, `max_escalations`, `max_children`) stay operator-declared
and outside any detector's reach. Otherwise a model that answers "drawing, tile
it sixteen ways" is deciding the bill — which is the failure `MaxEscalations`
exists to bound.

Sketch, if detection lands:

```json
"survey": {
  "descend": 2, "max_calls": 40,
  "when": [
    { "measured": "low_resolution", "tile": true, "descend": 2 },
    { "measured": "ruled_table",    "hint": "preserve row and column structure" },
    { "measured": "multi_column",   "descend": 1 }
  ]
}
```

`when` clauses route on measured predicates; the budgets above them are not
addressable from inside a clause.

## Related evidence

Model choice for this corpus is settled separately and measured — chandra-ocr-2
beats Qwen3-6-27B-MPT 23/24 checks to 18/24 at a third of the wall time, and
Qwen times out producing nothing on the E-size sheet. See `~/inflight/now.md`
and `bench/README.md`. Note also that chandra's sampling must stay greedy: a
repeat penalty cost a 12-digit auditor file number in 3 of 3 runs.
