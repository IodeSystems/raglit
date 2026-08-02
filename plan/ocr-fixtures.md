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

## Open

- **Types 2, 3 and 4 are unmeasured.** Everything above is one page.
- **Ground truth exists only for type 1.** For the rest, the available signal is
  agreement between independent readings — two engines, two resolutions, or the
  two copies of the type 4 drawing — which says where to look, not what is true.
- **A harness.** These runs were shell scripts in a scratch directory. Something
  repeatable belongs here before the next OCR change is justified by a number.
