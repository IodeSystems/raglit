# raglit: the OCR fixture set — pages that decide things

Status: in use 2026-08-02. Living doc.

Every OCR change in this repo so far was justified by ONE page. That page keeps
being right to use — it is the only one in the corpus with a human-verified
reading — but a single sheet cannot say whether a change helps a corpus. This is
the set that should.

The documents live in the ardley corpus (`~/life/projects/ardley-v-brannock`),
not in this repo: they are filed legal records and belong where they were filed.
What is recorded here is which pages, why each one, and what is known to be true
about it.

## The four page types worth separating

A corpus of filed documents is not one kind of image, and the failures are not
one kind of failure. These four break differently:

**1. Text and drawing on one sheet — `records/202205230090-2022-halvor-ROS-disputed.pdf` p1**

A recorded record of survey: notes, certificates and a boundary drawing, at ~3.6pt
(10 px of glyph height at 200 DPI, against 18-25 px for an ordinary page). Every
reader tried has misread it at 200 DPI.

GROUND TRUTH, established by a person re-reading at 150% and recorded as a
corrected page reading:

| fact | truth |
|---|---|
| surveyor | KEVIN G. HALVOR |
| certificate | 20123169 |
| note 2 auditor's file number | 202107080106 |
| deed auditor's file number | 200808180120 |
| recording number | 202205230090 |

This is the page that produced every OCR finding to date: the image-token
ceiling, adaptive render DPI, and the digit-stripped spelling assist.

**2. A number-dense table — same document, p2**

The EXISTING CORNERS table: each found monument and its offset from calculated
position ("FOUND 1/2\" REBAR 'SHMT' 0.2' N AND 0.2' M OF CALC'D POSITION"). 2,072
characters that a segmentation bug once dropped entirely. Nothing here is prose;
a reader that paraphrases produces plausible garbage.

**3. A scanned multi-page instrument — `evidence/icloud-2026-07-25/decoded/attachments/2021-05-24-PSA-OFFER-buyer-signed-30pg-MISNAMED-as-form22J__32945157.pdf`**

Thirty pages: form pages, initials, exhibits, and two Authentisign envelopes
whose stamps cover about half the pages each. It is the document this whole
identity feature is named after — stored under a filename that names a different
instrument — and ten of its pages were indexed as nothing but their signing
overlay before the text-layer decision was removed. Its Exhibit A pages carry
the legal descriptions.

Use it for what only a long document shows: consistency across pages, cost per
page, and whether a change that helps page 1 still helps page 27.

**4. A drawing with no prose — `correspondence/attachments/…Access_permit_-_Paul_Farley__1636_001.pdf` p4**

A hand-drawn site plan: dimension callouts, a road name, a parcel number, and
nothing to read as sentences (`196.53'`, `224.6'`, `35'0"`, `x25'`). The
dimensions are the content, and they are the kind of content a language model is
happiest inventing.

It has a second copy — `records/2021-02-havern-access-permit-AC21-0044-with-1993-qcd.pdf`
p4 — the same drawing filed twice. Two scans of one page make an agreement check
possible with no ground truth at all: where two independent readings of the same
drawing disagree on a dimension, at least one is wrong.

## What has been measured on them

Only type 1 so far, at 400 DPI with `--image-max-tokens 16384`:

| configuration | certificate | note 2 AFN | deed AFN | surveyor |
|---|---|---|---|---|
| 200 DPI, no assist (was production) | ✗ 201364 | ✗ | ✗ 2008081020 | ✗ IN DEED HALVR |
| 400 DPI, no assist | ✓ | ✗ | ✓ | ✗ HALVR |
| 400 DPI + tesseract text, whole | ✗ 20123164 | ✗ | ✓ | ✓ HALVOR |
| 400 DPI + tesseract words, digits masked | ✓ | ✓ | ✓ | ✓ |
| 400 DPI + tesseract words, digits deleted | ✓ | ✓ | ✓ | ✓ |

Two things that fell out and should not be re-litigated: handing the model
tesseract's numbers makes it adopt them over its own correct reading, and an
instruction to prefer the image does not stop that. And the difference between
masking a number and deleting it is nothing — the marker is not load-bearing,
which was tested because the opposite had been assumed.

## 2026-08-03 — all four fixtures, one harness

Everything above compares numbers taken by different scripts at different times.
This is the four fixtures, six configurations, one client, one prompt, one set of
checks. `Qwen3-6-27B-MPT` at temperature 0. `assist` is `spellingAssist`
reproduced exactly — tesseract's words with every digit run masked.

| config | drawing | exhibit | corners | facts | total |
|---|---|---|---|---|---|
| plain | 4/4 | 1/1 | 4/4 | 3/5 | 12 |
| **assist** | 4/4 | 1/1 | 4/4 | **5/5** | **14** |
| rot +2° (fixed canvas) | 4/4 | 1/1 | 4/4 | 3/5 | 12 |
| zoom 1.05x | 4/4 | 1/1 | 4/4 | 4/5 | 13 |
| CLAHE clip 2.0 | 4/4 | 1/1 | 4/4 | 4/5 | 13 |
| assist + rot +2° | 4/4 | 1/1 | 4/4 | **5/5** | **14** |

**Assist wins, and nothing geometric reaches it.** The best single facet recovers
one of the two facts `plain` misses; assist recovers both. Stacking rotation on
assist adds nothing.

### THREE OF THE FOUR FIXTURES ARE SATURATED

`plain` already scores full marks on the drawing, the corners table and the
exhibit. Every configuration ties on them, so the entire table separates on ONE
page, and so does every conclusion drawn from it.

Worse, the checks pass while the reading is wrong. Reading the EXISTING CORNERS
crop by eye against `corners`' 4/4:

| printed | 27B returned |
|---|---|
| `S 31°05' E 0.4' FROM CALC` | `5.31' 0.5' E 0.4' FROM CALC` |
| `S 47°44'E 2.0' FROM CORNER` | `5.47' 0.44' E 2.0' FROM CORNER` |
| `0.1'S AND 0.1'W OF CALC` | `0.15' AND 0.14' W OF CALC` |
| `MOWRER` (x5) | `MOWER` |

Two bearings turned into distances and an offset gained a decimal place that is
not on the page, and `EXISTING CORNERS`, `REBAR`, `CALC`, `202205230090` all
still matched. **A substring check cannot measure a transcription.** The fixture
set needs facts read by a person, the way type 1's were — which is the only
reason type 1 can measure anything at all.

### The geometry lever is RESAMPLING, not rotation

An earlier pass concluded that ±2° of skew recovered note 2's auditor file
number. It did not. That rotation used `expand=True`, which grows the canvas, and
the growth was doing the work:

| variant | size | facts |
|---|---|---|
| original | 3400x4400 | 3/5 |
| rot +2°, canvas expands | 3552x4516 | **5/5** |
| rot +2°, canvas fixed | 3400x4400 | 3/5 |
| **resize to 3552x4516, NO rotation** | 3552x4516 | **5/5** |
| rot +2°, PIL, no expand | 3400x4400 | 4/5 |

Both 5/5 rows are the ones at 3552x4516 and neither depends on the angle. **A
4.5% bicubic upscale matches assist on this page for nothing** — no second
engine, no added prompt.

It is dimension-SPECIFIC and not monotonic: 3570x4620 scores 4/5 while 3552x4516
scores 5/5. Which is consistent with a page at the resolution limit landing on
the encoder's patch grid one way or another, and is not explained.

### The region toolkit, run as a system

Not one facet at a time — `raglit regions --depth 2`: descent, per-region
rotation, tiling, and the transform judge.

| | facts | chars | distinct chars | regions | rotated |
|---|---|---|---|---|---|
| assist, one pass | 5/5 | 7,593 | 7,392 | 1 | — |
| **toolkit, depth 2** | **5/5** | **11,652** | **11,225** | 15 | 8 |
| toolkit, drawing | 4/4 | 2,130 | — | 4 | 0 |

**It ties assist on the facts and reads about 50% more of the sheet** — 1%
duplicate lines in both, so the extra is coverage, not repetition. Eight of
fifteen regions took a rotation, which one pass structurally cannot do.

It cost 15 model calls against assist's one call plus a ten-second tesseract run,
and seven regions hit the call ceiling still wanting more. Per KNOWN FACT
recovered, assist is roughly 15x cheaper. What the toolkit buys is the ~3,800
characters of drawing interior that assist never transcribes at all.

So the two are not competitors. Assist gets the known facts off a page cheaply;
the descent gets the whole sheet expensively, and which one a document wants is a
property of the document.

`transform-suspect` and `faded` both fired on real pages on first contact.

## Open

- **Types 2, 3 and 4 cannot currently BE measured.** They are saturated: `plain`
  scores full marks on all three, so no configuration can be distinguished on
  them. Their checks also pass on demonstrably wrong readings — see 2026-08-03.
  Fixing this means facts read by a person, as type 1 has.
- **Ground truth exists only for type 1.** For the rest, the available signal is
  agreement between independent readings — two engines, two resolutions, or the
  two copies of the type 4 drawing — which says where to look, not what is true.
- **A harness.** These runs were shell scripts in a scratch directory. Something
  repeatable belongs here before the next OCR change is justified by a number.
