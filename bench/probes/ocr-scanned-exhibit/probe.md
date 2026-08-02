---
name: ocr-scanned-exhibit
class: capability
requires: { modality: image }
run: warm
limits: { maxTurnsPerStage: 2, maxToolCallsPerStage: 0 }
---

# One page of a thirty-page scanned instrument

A page from the purchase and sale agreement that this corpus stores under a
filename naming a DIFFERENT instrument — the document that motivated document
identity in the first place.

It is here for the property none of the other fixtures have: it is one page of
many, from a document signed under two e-signature envelopes whose stamps cover
about half the pages each. Ten of its pages were once indexed as nothing but
their Authentisign overlay, because a text layer said 46 characters were present
and a threshold believed it. The page is a scan; the overlay is the only real
text layer on it.

A model that reads the stamp and stops has produced exactly the failure that put
this document out of reach for a year. The checks therefore look for the
document's own words, not the envelope id.

## Prompt

Transcribe all text visible in this document page image exactly as it appears, preserving reading order and line breaks. Output ONLY the transcription.

![one page of a scanned, e-signed purchase and sale agreement](_fixture/page.png)

## Checks

- response_contains: Authentisign
- response_not_contains: I cannot
- response_not_contains: unable to read
