# raglit: schemaed ingest, proved on a corpus that is not ours

Status: ◐ in progress, started 2026-08-18. Living doc — prune as slices land.

How this plan works: see `~/CLAUDE.md` § Planning. Status marks: ◻ todo ·
◐ in progress · ✅ done · ⏸ parked · ❓ blocked. Completed trees move to
`plan/done/`.

## Goal

`plan/schemaed-documents.md` ends by saying what it cannot say:

> Every test here drives a FAKE chatter returning a canned string. That validates
> the plumbing … and it validates none of the thing the feature actually IS.

This is the run that answers it. Not a demo — a measurement, against a corpus
nobody here authored, with FDA's own metadata as the answer key.

Three questions, in the order they can be answered:

1. Does a model shown real forms propose a schema worth keeping?
2. Is `doc_type` resolution accurate enough to act on — including the negative
   case, a document that is NOT one of the types?
3. Do extractions come back CORRECT, as opposed to merely well-shaped?

## The corpus

50 records from FDA's Office of Inspections and Investigations FOIA electronic
reading room, selected 2026-08-18. At `~/local/dataset/fda-483/`.

The listing is served as JSON (`fda.gov/datatables-json/ora-foia-reading.json`,
3,164 rows), which is what made a stratified pick possible rather than a scrape
of whatever the first page held.

- **40 are Form FDA 483**, "Inspectional Observations" — one form, issued at the
  close of an inspection, by every district, for decades. Spread deliberately
  over **41 establishment types** (sterile injectable manufacturers through to
  shell egg producers), **22 states**, and **2012–2026**. Same purpose, wildly
  different paper.
- **10 are near-misses**: 483 Responses (the firm's letter back), Untitled
  Letters, State Referral Letters, Establishment Inspection Reports, Amended
  483s. These are the point, not padding — a corpus of nothing but forms cannot
  tell you whether the model over-claims. See slice 4.
- Files are named by FDA media id (`193899.pdf`). Uninformative on purpose: it
  is the scanner-name condition `document-identity.md` exists for, and a
  descriptive filename would hand the caption away for free.
- Mixed provenance. Some born-digital; several are scans whose Acrobat "Paper
  Capture" text layer is visibly wrong — `FOOD AND DRUO ADMINJSTRATION`,
  `DATE(S) OF l'ISPECTIOH`. Precisely the case that ended the text-layer
  heuristic (`document-identity.md`, "the upstream rule this forced"). This
  corpus can confirm that decision paid.

### truth.json — why this corpus and not another

FDA publishes, next to each file, its own metadata: company name, FEI number,
record date, record type, state, establishment type. It is INDEPENDENT of
anything read off the page, so an extraction can be scored rather than admired.

The answer key is NARROWER than it first looks, and both narrowings were found
by reading disagreements rather than by planning:

- **Usable as truth**: `fei_number` (an exact string, published beside the file
  and read off the page) and `firm_name` (normalised — FDA's feed is
  HTML-escaped, `Becton Dickinson &amp; Company`, and a naive compare marks a
  correct reading wrong).
- **NOT usable — `establishment_type`.** Measured on 193899: FDA's listing says
  `Shell Egg Producer`, the document's own TYPE ESTABLISHMENT INSPECTED cell
  says "Steamed and frozen processed egg products, fish based surimi products".
  Both are right; the category is coarser than the page.
- **NOT usable — the DATE.** Measured on 106817: the document says the
  inspection ran 5/15/2017–7/6/2017 and was issued 7/6/2017, and FDA's
  `field_record_date` says 09/27/2018. It is a RECORD date, not an inspection
  date. This plan originally listed it as usable truth; it is not.
- **No key at all — `observations`.** The half of the form with the most content
  and the least verification, and a nested array is exactly what
  `agent.SchemaValidator` does not enforce. A fabricated observation list is
  what this run cannot catch, and the result says so rather than reporting a
  coverage number over it.

Two fields, then. Scoring the other three would mark correct readings wrong,
which is worse than not scoring them. `score.py` in the corpus directory carries
each of these reasons next to the field it excludes.

## Set up (✅)

- `bin/raglit` built from `feat/document-and-index-identity` @ `257ff03`. The
  running daemon (`4a09e16`, 2026-08-16) predates `doctype`/`fields` entirely,
  so this runs EMBEDDED from a project-local home. No `daemon_url`, no project
  name, nothing routed. The 11 live indexes are untouched and stay that way.
- `~/local/dataset/fda-483/.raglit/` — home, index `fda483`, no `--embed`
  (lexical only; whether a paraphrase belongs in the vector space is an open
  question in `document-identity.md` and is not this run's to answer).
- **The hint is recorded** (`hint.txt`, 33 lines): the header-grid layout and
  that labels and values sit on separate lines, FEI vs case number, DATE(S) OF
  INSPECTION vs DATE ISSUED, who "individual to whom report issued" is, that
  letters and EIRs are in here too, and that a baked-in text layer on these
  scans is not to be trusted.
- **3 gold documents ingested**, 22 pages, 12 fragments: an Outsourcing Facility
  (2015, scanned), a Finished Pharmaceutical Manufacturer (2019, no text layer),
  a Shell Egg Producer (2026, no text layer).
- **The read is accurate.** 193899 p1 returned FEI `3004323569` — exact match to
  FDA's listing — with the firm name, the inspection range 3/12–3/13/2026 and
  numbered observations all intact, off a pure scan. `local-Qwen3.8-27B` is
  adequate as an OCR engine on this corpus.

## Active work

### ❓ 1. Every ask is capped at 800 output tokens, including the two that
### cannot be

**ROOT CAUSE FOUND 2026-08-18. Blocks slice 5 and needs a decision.**

    $ raglit doctype propose "483" 94787.pdf 122717.pdf 193899.pdf
    raglit: propose doc type: arguments are not a JSON object: unexpected end of JSON input

Bisected:

| gold documents | outcome |
|---|---|
| 1 | ✅ schema WITH a nested `observations` array |
| 2 | ✅ schema without the array, a `total_observations` count instead |
| 3 | ❌ truncated mid-JSON |

`identity.go:373` sets `identityMaxTokens = 800`, and `askWith` (identity.go:596)
applies it to EVERY ask. The constant's own comment carries the reasoning:

> Unlike transcription this is not a re-emission of the input — it is three
> short fields — so the cap is a constant.

True of identity and of tags. **False of the only two asks whose output size is
the CORPUS OWNER's rather than raglit's:**

- `ProposeDocType` (doctype.go:342) emits a description, an extraction prompt
  AND a whole JSON Schema. The one-document proposal was already ~800 tokens.
- `ExtractFields` (docfields.go:330) emits a filled-out schema. **Every schemaed
  extraction in raglit is capped at 800 output tokens.** A 483 whose
  `observations` array is a dozen paragraphs does not fit, and the design's own
  headline case — "a hundred of those are worth far more as a hundred RECORDS" —
  is exactly the shape that overflows.

`unexpected end of JSON input` IS the signature of a cut-off JSON. And the fix
loop cannot recover it: truncation is not repetition, so `loopBreakSampling`
never fires, the retry re-asks with the same cap, and it fails identically until
the attempts run out.

**This is the first thing the live run found that the fake-chatter tests could
not, and it would have silently wrecked slice 5** — the documents that overflow
are the ones with the most content, so the corpus would score well on the thin
records and fail on the substantial ones.

- **✅ FIXED 2026-08-19** (user's call, uncommitted). `Identifier.maxTokens` is
  per-ask and defaults to `identityMaxTokens`; `ProposeDocType` sets
  `proposeMaxTokens` (4000) and `ExtractFields` sets `fieldsMaxTokens` (8000).
  Both already built a per-call sub-`Identifier`, so there is no shared state to
  race. `explainCap` now names the ceiling when an answer ends mid-JSON, because
  the validator can only report that the arguments did not parse — which sends
  the reader after the model instead of after the cap. `indextext.go` records
  the segmenter learning the same lesson the slow way.
- **tested without a live model**: `TestAnswerCap_IsPerAskAndNotOneConstant`
  records the `ChatOpts` each of the four asks is made under — identity and tags
  keep 800, propose and extract get their own — the way `recipe_test.go` asserts
  the recipe carries every term. A fake chatter cannot truncate, but it can
  record what it was asked with. `TestAnswerCutOff_NamesTheCapAndNotJustTheSymptom`
  drives a reply that stops mid-object and requires the error to say so.
  Full suite green.
- **the 2-document proposal is usable meanwhile** and is kept in `propose2.log`.
  It caught a real corpus artifact unprompted — "'FEI NUMBER' … may be labeled
  'FBI NUMBER' in scans" — which is what proposing from several examples is for.

**The one-document proposal is worth keeping** (`propose1.log`): every
identifier typed as a string, `required` held to `fei_number`/`firm_name`/
`observations`, an `observations` array of `{number, text}`, and an extraction
prompt carrying the hint's own distinctions in its own words — "'FEI NUMBER' is
the establishment identifier, not a case number", "a firm representative, not an
FDA agent". **That is the first live evidence the hint reaches a real model**;
`indexhint_test.go` could only prove the string was appended.

- **next**: bisect it — two documents, then three with the shortest gold set.
  If it is length, `identityExcerpt` already truncates per document but nothing
  bounds the SUM, and three 15-page 483s is the case that finds out.
- **risks**: if it is the 27B's tool-calling degrading on a long prompt rather
  than a raglit bound, the fix is a smaller gold set, not a code change — and
  the error message is wrong either way. "arguments are not a JSON object" names
  the symptom; "the model returned no arguments after N attempts" names the
  cause, and only one of those tells you to shorten your input.
- **assumption to check**: that a proposal from ONE document is good enough to
  proceed on. It reads well, but proposing from several examples is exactly what
  stops a schema being a transcription of one document's quirks — and this one
  came from the shell egg producer, the least typical document in the set.

### ✅ 2. The `483` type is registered

The 2-document proposal, extracted from `propose2.log` into `type-483.json` and
registered with `doctype add --file` — the reviewed path, not `--save`.
11 fields, 5 required (`dates_of_inspection`, `fei_number`, `firm_name`,
`type_establishment_inspected`, `total_observations`).

**Registered BEFORE any identity ran**, so no document is stranded at
`doc_type=''` and the `identify --force` backfill at the end of
`schemaed-documents.md` is not owed here at all.

Kept on review, with reasons:

- `employee_signature` — the FDA investigator, from a handwritten block. Kept
  deliberately even though it is the field most likely to be invented: it is not
  required, a blank costs nothing, and whether the model guesses at handwriting
  is worth knowing.
- `total_observations` as a NUMBER, not a string. The "identifiers are strings"
  rule is about identifiers; a count is a count. It is `required`, so a document
  whose observations cannot be counted will fail validation and retry — accepted.

### ✅ 3. The corpus is ingested

50 documents, **325 pages, 31 minutes** — well under the 1.5–2h estimated. The
endpoint threw 503s at the start and `429 — 1/1 slots busy, queue-timeout`
throughout; raglit's backoff rode all of them out and no document failed.

**The hint is frozen from here until the run reports.** It is in the reading
recipe, so editing it now invalidates all 325 pages. If it turns out to be
wrong, that is a finding to record, not a thing to quietly fix mid-corpus.

### ❓ 4a. STOPPED 2026-08-19 — identity fails on every document, and it is a
### SECOND failure mode, not the cap

    ✗ 159323.pdf: identity: arguments are not a JSON object:
      invalid character '/' looking for beginning of value

**23 errored, 0 captioned, 24 pending when it was stopped.** Stopped
deliberately: the remaining 24 were going to fail identically and spend GPU
doing it.

Not truncation — the arguments do not END early, they START wrong, with a `/`
where a `{` belongs. So `extractJSON` handed the validator something that is not
the tool call: the model is emitting a leading token the extractor does not
strip. It is NOT the token cap, and the cap fix did not cause it (identity was
never seen to succeed on this corpus — the 3 gold documents ingested at the very
start also finished `0 of 3 named`).

- **✅ CAUSE FOUND AND FIXED 2026-08-19.** `extractJSON` (segment.go) took the
  first `{` to the LAST `}` while its own comment claimed it took the first
  object. The delano index — a different corpus entirely — had **290 of 817
  identity jobs failed, 239 with `invalid character 'X' after top-level value`**,
  which is the same bug seen from the other side: a reply carrying anything after
  the object came back as a span holding both. Here the reply held no object at
  all, so the whole thing came back and the error named the first character of
  the prose. It now takes the first BALANCED object, string-aware, preferring one
  that parses; see `plan/ui-redesign.md` §0a for how it was found.
- **note**: this appeared with the doc_type enum in the prompt (registered
  between the gold ingest and this sweep), so the ask CHANGED shape — but the
  gold documents failed to caption before any type existed, which argues against
  the enum being the cause and for something simpler.
- **the corpus is fine.** 50 documents, 325 pages, all transcribed and indexed.
  Everything before identity stands; only the captioning half is stuck.

### ◻ 4. Identity, and the negative case

`raglit identify` captions each document and resolves its type; the worker then
chains each extraction as the caption closes.

- **next**: run identity over a handful of near-misses FIRST, before the 40.
- **what is being measured**: whether a 483 Response — a letter on the firm's
  letterhead that quotes the observations it is answering — resolves as `""` and
  not as `483`. If it resolves as a 483, the empty enum member is not doing its
  job and the corpus will fill in records for documents that are not forms.
  This is the cheapest possible check and it gates everything after it.

### ✅ 5. RESULTS, 2026-08-22

50 documents, 325 pages, **all 50 captioned, 0 identity errors** — against 100%
failure before the `extractJSON` fix. That is the live proof that was missing.

**Extraction is not the weak link. Resolution is.**

| | |
|---|---|
| 483s in the corpus | 40 |
| resolved as type `483` | **24 (60%)** |
| extracted | 24 |
| `fei_number` | 23 right · **0 wrong** · 1 blank |
| `firm_name` | 21 exact · 3 flagged · 0 blank |
| near-misses correctly declined | **10 of 10** |

**Zero wrong values in 24 records.** All three flagged `firm_name` rows are
disagreements with the ANSWER KEY, not misreadings:

- `Aurobindo Pharma Limited Unit IX` vs FDA's `Aurobindo Pharma Unit 9` — the
  page uses a roman numeral, the listing normalised it.
- `SMS PHARMACEUTICALS LIMITED, Unit-VII` vs FDA's `SMS Pharmaceutical Limited
  - Unit VII` — the listing dropped the plural.
- `Cal-Maine Foods, Inc.` vs FDA's `Cal-Main Foods, Inc.` — **the listing has a
  typo.** Cal-Maine is the company.

So the assumption recorded at the bottom of this plan — "that FDA's listing
metadata is correct; nothing here checks IT" — is now MEASURED AND FALSE. The
answer key has errors in it, and the reading was right where they disagreed.

The one blank `fei_number` is the designed failure, not a miss: page 1 of 83317
carries the `FEI NUMBER` label with no value beside it in the transcript, and the
model left it empty rather than inventing one.

**The negative case is perfect.** All ten near-misses — 483 Responses, Untitled
Letters, State Referral Letters, EIRs, Amended 483s — declined the type. Zero
over-claims. The empty enum member does its job.

**The finding that matters: 16 of 40 genuine 483s resolved NO type.** They are
captioned correctly and carry no `doc_type`, so no extraction ever chained, and
**nothing anywhere reports it** — a document that should have a record and does
not looks exactly like one that was never meant to have one. No era pattern; the
misses are scattered across 2016–2024, so it is not degraded scans. This is the
sharp version of the open item at the end of `schemaed-documents.md`.

**`doc_type` beat `kind`.** 193608 came back `kind=court filing` — plainly wrong
for an FDA inspection form — and still resolved `doc_type=483` and extracted
correctly. The per-corpus authored vocabulary outperformed the general closed
one, which is an argument for the schemaed-documents design. Related: 20 of the
483s landed on `kind=certification`, a term that fits nothing in this corpus
well. `document-identity.md` says a corpus that keeps reaching for an awkward
term means the vocabulary is wrong; this is that signal.

- **next**: find why resolution misses 40%. The identity call is shown the type
  NAME and its one-line description; whether the description proposed from two
  gold documents generalises is the first thing to test.
- **optional**: a `doc_type` coverage warning — "16 documents look like a
  registered type and carry none" is computable and would have said this without
  a corpus built to measure it.

### ◻ 5b. Extract, then SCORE (superseded by the results above)

- **next**: `raglit fields`, then a scorer over `truth.json`: exact match on
  `fei_number`, normalised match on `firm_name`, date within the inspection
  range. Report WRONG apart from MISSING — a blank field is a document that did
  not say, a wrong field is a record that lies.
- **risks**: `observations` is a nested array of objects, which
  `agent.SchemaValidator` does not enforce (it checks required keys and
  top-level types only — `schemaed-documents.md`, "Open / not done"). The
  observations are the half of the form with no answer key, so a malformed or
  invented observation list is exactly what this run cannot catch. Say so in the
  result rather than reporting a coverage number over it.
- **optional extension**: `raglit similar` over the 40. Several are the same
  firm inspected twice; whether the near-duplicate machinery pairs them is free
  to check once the corpus is indexed.

## Decisions taken 2026-08-18

- **One model in all three roles.** corrallm serves one local model at a time
  (`slots: 1`). `local-chandra-ocr-2` 503'd nine times trying to load beside
  `local-Qwen3.8-27B`, so OCR on chandra and identity on Qwen would swap models
  once per document. `local-Qwen3.8-27B` now serves vision, segmentation and
  identity. Verified adequate as OCR (above). `chandra-ocr-2` remains the
  fallback if a page reads badly, at the cost of the swap.
- **40 + 10, not 50 of one thing.** A monoculture cannot test resolution.
- **The corpus lives outside the repo**, at `~/local/dataset/fda-483/`. 62 MB of
  third-party PDFs is not repository content.

## Found on the way

- **The hint reaches a live model and changes its output** — first evidence,
  see slice 1. Worth folding back into `schemaed-documents.md` when this run
  reports, because that doc currently claims only the fake-chatter version.
- **`~/.raglit/config.json` names models the endpoint no longer advertises**:
  `chandra-ocr-2`, `Qwen3-6-27B-MPT`, `nomic-embed-text` against the endpoint's
  `local-chandra-ocr-2`, `local-Qwen3.8-27B`, `local-nomic-embed-text`. That is
  the DAEMON's config and affects the live indexes, not just this test. Not
  touched — a real config is the user's. **This is the one item here that is not
  about the test corpus and should not wait on it.**
- **`raglit index` has no `--fresh`**; it is an `ingest` flag. The help text
  documents `--fresh` under `ingest` and `index` exits 2 on it, which is correct
  but reads as a missing feature rather than a misplaced flag.

## Blocking decisions (USER owns)

- **Nothing blocks slices 2–5.** Named here so the next session does not go
  looking: the chandra-vs-swap tradeoff and the corpus location are both
  recorded as taken above, and either can be revisited without redoing work
  EXCEPT that changing the OCR model changes the reading recipe and costs a
  re-ingest of whatever has been read by then.

## Assumptions recorded

- That `local-Qwen3.8-27B` stays resident for the run. If corrallm evicts it for
  another workload mid-ingest, pages read either side of the eviction were read
  by different models and `ocr_pages.model` is what says so.
- That FDA's listing metadata is correct. It is the answer key; nothing here
  checks IT. A disagreement between page and listing is at least as likely to be
  a stale listing row as a bad read, and the scorer should present both rather
  than declare a winner.
- That 40 documents is enough to say something about accuracy. It is enough to
  find systematic failure and not enough to put a confidence interval on a rate.
