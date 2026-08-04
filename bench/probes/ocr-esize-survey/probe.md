---
name: ocr-esize-survey
class: capability
requires: { modality: image }
run: warm
limits: { maxTurnsPerStage: 2, maxToolCallsPerStage: 0 }
---

# The sheet that does not fit in one look

A 27 x 36.7 inch record of survey — 991 square inches, 10.3 letter pages. This is
the page the whole hierarchical-regions design was opened for, and until now it
was described in `plan/hierarchical-regions.md` and measured by nothing.

Every other fixture in this set is 8.5 x 11. That matters more than it sounds:
the region reader's tiling path fires only on a region flagged `low-resolution`,
which is `area > ~205 sq in`, so **no other probe here can reach that code at
all**. A letter page is never tiled, never escalated, and never exercises the
part of raglit that exists for sheets like this one.

## Why this page breaks readers

The encoder's budget is per IMAGE. Measured against a live endpoint: a letter
page costs 3678 image tokens and this sheet costs 4011 — 12x the pixels for 9%
more tokens, so it is seen at roughly a quarter scale and six-point surveyor
lettering falls below the patch grid.

What that produced was not an error. It was tidy prose that had replaced the
entire legal description with a one-line figure caption and invented a plausible
auditor's file number. The repetition guard fired twice, both times on text that
GENUINELY recurs — the monument callout 23 times, a bearing call 30 times —
because at that scale one instance is indistinguishable from the next.

## What is known to be true

Established by reading the sheet, and by the corrected record of the failure it
caused:

| fact | truth |
|---|---|
| the quitclaim's auditor file number | `9308270057` |
| the cap stamped on the found rebar | `MOWRER` |
| the surveyor's cap | `SUMMIT` |
| the strip the description turns on | westerly of the centerline of the right-of-way |

`A#200308270057` is the specific invention this page produced for that file
number — a real number with `200` prepended and a digit dropped. It is refused
rather than merely absent, because a reader that returns it is not failing to
read, it is confabulating a recording that does not exist.

## Prompt

Transcribe all text visible in this document page image exactly as it appears, preserving reading order and line breaks. Output ONLY the transcription.

![a 27x36.7 inch record of survey: boundary drawing, monument calls, legal descriptions](_fixture/page.png)

## Checks

- response_contains: 9308270057
- response_contains: MOWRER
- response_contains: SUMMIT
- response_contains: WESTERLY
- response_not_contains: A#200308270057
- response_not_contains: 200308270057
