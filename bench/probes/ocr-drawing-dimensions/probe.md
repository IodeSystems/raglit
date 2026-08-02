---
name: ocr-drawing-dimensions
class: capability
requires: { modality: image }
run: warm
limits: { maxTurnsPerStage: 2, maxToolCallsPerStage: 0 }
---

# A drawing whose dimensions are the content

A hand-drawn site plan filed with a county access permit: a road, a parcel
outline, and dimension callouts. There is no paragraph anywhere on it, and the
numbers are the entire point — they are what a setback argument is made of.

This is the page type a language model is happiest inventing on. Given a sketch
and no sentences, a fluent describer will produce "a site plan showing the
property boundary and driveway access" — true, useless, and containing none of
the measurements. The checks are dimensions for exactly that reason.

The same drawing is filed TWICE in this corpus, in a permit packet and in an
email attachment. Two scans of one page make an agreement check possible without
any ground truth: where two independent readings disagree about a dimension, at
least one is wrong. That comparison is not this probe — this one asks whether
the numbers arrive at all.

## Prompt

Transcribe all text and every dimension visible in this drawing exactly as it appears. Include every measurement, label and annotation. Output ONLY the transcription.

![a hand-drawn site plan with dimension callouts](_fixture/page.png)

## Checks

- response_contains: 196
- response_contains: 224
- response_contains: 203
- response_contains: Brannock
