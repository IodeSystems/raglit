# raglit OCR bench

Runs raglit's OCR fixtures through corrallm's `llm-bench`, so a change to how
pages are read is justified by a number somebody else can reproduce.

    llm-bench run --config ~/.corrallm/llm-bench.yaml --tasks-dir bench/probes

Every probe declares `requires: { modality: image }`. A model that cannot see
pixels is not a candidate here, and llm-bench skips it rather than scoring a
model on prose it invented from a filename.

## Which fixtures are committed, and which are not

The pages come from a live legal corpus (`~/life/projects/ardley-v-brannock`),
and THIS REPOSITORY IS PUBLIC. A fixture committed here is world-readable and
permanent, so the test is not "is it useful" but "is it already published".

COMMITTED — `ocr-survey-facts`, `ocr-survey-corners`. Both pages of a record of
survey recorded with the Havern County Auditor under 202205230090. A recorded
land record is public by operation of law; anyone can pull it from the county,
and the copy here publishes nothing the recording does not. These are also the
only fixtures with verified ground truth, so committing them is what makes the
bench reproducible by someone who does not have the corpus.

NOT COMMITTED, generated locally:

- `ocr-drawing-dimensions` — a county access permit application. Arguably a
  public record, but the sheet carries a third party's HOME ADDRESS, and that
  party is an opposing party in live litigation. County file, yes; GitHub, no.
- `ocr-scanned-exhibit` — a signed purchase and sale agreement. A private
  contract with signatures and initials on it. Not public in any sense.

Regenerate the uncommitted ones (and refresh the committed ones) with:

    bench/make-fixtures.sh                     # renders from the default corpus
    RAGLIT_BENCH_CORPUS=/path/to/docs bench/make-fixtures.sh

Without a fixture the probe cannot run, and that is the intended failure: the
bench refuses rather than measuring a placeholder. A clone without the corpus can
still run the two survey probes, which are the ones that carry ground truth.

## What each probe is for

See `plan/ocr-fixtures.md` for why these four pages and what is known to be true
about them. In short: text-and-drawing on one sheet, a number-dense monument
table, a hand-drawn plan whose dimensions ARE the content, and one page of a
thirty-page scanned instrument.

## Reading a result

The checks are FACTS, not similarity. Each `response_contains` is a string a
person established by reading the page — a certificate number, an auditor's file
number, a surveyor's name. Each `response_not_contains` is a specific WRONG
reading that a model has actually produced here, so a regression names itself
instead of showing up as a score that drifted.

That makes the bench narrow on purpose. It answers "did this configuration read
the facts we know", not "is this transcription good".

## First run (2026-08-02, Qwen3-6-27B-MPT, warm)

    ocr-survey-facts   14738 prompt tokens, 53s, 334 tok/s   5/7 checks
      PASS  20123169         the certificate number
      FAIL  202107080106     note 2's auditor file number
      PASS  200808180120     the deed's auditor file number
      PASS  202205230090     the recording number
      FAIL  LISSER           read as LISER
      PASS  not 2008081020   the misreading a person corrected by hand
      PASS  not 201364       what this model returned at 200 DPI

That surveyor-name row was recorded as `HALVOR` / read as `HALVR`, and both were
the repository's pseudonym rather than anything on the page or in the response.
The check could not pass at any configuration; corrected 2026-08-03. The score
is unchanged — the model did miss the name — but it missed it by normalising
`LISSER` to `LISER`, which is the failure the check is for.

Which is the manual measurement, reproduced: at 400 DPI with the raised token
ceiling the model recovers the certificate and the deed's file number on its
own, and still normalises the surveyor's name and misses note 2.

Both remaining failures are what raglit's `ocr.mode: assist` fixes — the cheap
engine's WORDS, with its numbers stripped. That path is NOT what this probe
measures, and cannot be: a probe is a static prompt, and the assist text is
generated per page from tesseract's reading of it. Measuring it needs a
generated companion probe (make-fixtures.sh would write the word list into the
prompt), which is the obvious next piece and is not built.

So read a run of this bench as "what does the MODEL do with a page", not "what
does raglit do with a page". The first is what a model swap changes; the second
is what a pipeline change changes, and they are different questions.
