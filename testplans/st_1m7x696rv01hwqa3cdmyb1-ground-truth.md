# Ground-truth sheet — the three saturated OCR fixtures

For the operator to confirm, correct and complete, then hand back. Every row
marked **seeded** is a reading the repository already records in
`docs/design/hierarchical-regions.md` (2026-08-03) or
`docs/design/ocr-fixtures.md` (2026-08-03); every other row is a blank that
exists only when a person has read the page.

The privacy line (from `bench/README.md`): a committed check is a fact a person
read off the page, recorded in the repository's pseudonymized vocabulary where a
name appears — the surveyor's name is `DACE`, and the 2026-08-03 incident was a
check keyed to the un-pseudonymized prose. No new personal names, street
addresses, or party identifiers go in a probe. `ocr-drawing-dimensions` and
`ocr-scanned-exhibit` are NOT committed fixtures (their pages stay rendered
locally), but their checks are tracked text and get the same treatment.

## 1. `ocr-survey-corners` — survey p2, the EXISTING CORNERS table

COMMITTED fixture. Current checks: `EXISTING CORNERS`, `REBAR`, `CALC`,
`201907220018` — all true, all kept; the table below is added on top.

### Facts a person read (→ `response_contains`)

| row | fact as printed | status |
|---|---|---|
| J's bearing | `S 31°05' E 0.4' FROM CALC` | seeded — hierarchical-regions.md 2026-08-03, "reading the crop by eye" |
| K's bearing | `S 47°44'E 2.0' FROM CORNER` | seeded — ditto |
| L's offsets | `0.1'S AND 0.1'W OF CALC` | seeded — ditto |
| the cap stamp | `MOWRER` | seeded — ditto; whole-sheet reads normalise it to `MOWER` |
| S's reference | `DACE 20123169` | seeded — ditto (pseudonymized; the sheet is the surveyor's own) |
| full table rows (monument, type, bearing/distance, offset from calculated) | TO BE READ BY OPERATOR | not in the repo; the four seeded rows are the seed, the rest of the table is the operator's |

### Known-bad readings (→ `response_not_contains`)

Each is a transcription a model has ACTUALLY produced on this page
(ocr-fixtures.md, 2026-08-03, against the `corners` 4/4):

| misreading | what it was | status |
|---|---|---|
| `5.31' 0.5' E 0.4' FROM CALC` | J's bearing `S 31°05'` turned into a distance | seeded |
| `5.47' 0.44' E 2.0' FROM CORNER` | K's bearing `S 47°44'E` turned into a distance | seeded |
| `0.15' AND 0.14' W OF CALC` | L's offsets gaining a decimal not on the page | seeded |
| `MOWER` | the cap stamp normalised — note: `MOWER` is also a word the page could contain elsewhere, so this one is only safe as a check if the operator confirms the page contains `MOWER` nowhere | seeded, needs operator confirm |

## 2. `ocr-drawing-dimensions` — access-permit site plan p4

NOT COMMITTED (the sheet carries a third party's home address in live
litigation). Current checks: `196.53`, `224.6`, `203.1`, `196.23`.

### Facts a person read (→ `response_contains`)

| row | fact as printed | status |
|---|---|---|
| dimension 1 | `196.53` | seeded — current check; no person-verified reading recorded in the repo beyond this |
| dimension 2 | `224.6` | seeded — ditto |
| dimension 3 | `203.1` | seeded — ditto |
| dimension 4 | `196.23` | seeded — ditto |
| `35'0"` and `x25'` (the two other callouts ocr-fixtures.md mentions) | TO BE READ BY OPERATOR | mentioned in prose, never verified; confirm as printed, with the foot/inch marks |
| every other dimension callout | TO BE READ BY OPERATOR | |
| the road name | TO BE READ BY OPERATOR | the doc says there is one; the name is not recorded |
| the parcel number | TO BE READ BY OPERATOR | ditto |

### Known-bad readings (→ `response_not_contains`)

| misreading | status |
|---|---|
| none recorded in the repo for this page | TO BE READ BY OPERATOR — the optional two-copy agreement check (this drawing is filed twice: permit packet and email attachment) is the substitute signal while no misreading is known |

## 3. `ocr-scanned-exhibit` — PSA p28

NOT COMMITTED (a private signed contract). Current checks: `Authentisign`
plus the refusals `I cannot`, `unable to read`.

### Facts a person read (→ `response_contains`)

| row | fact as printed | status |
|---|---|---|
| the overlay | `Authentisign` | seeded — current check, and the reason the page is here |
| a small set of the instrument's own words (not the stamp) | TO BE READ BY OPERATOR | this is the gap: the probe currently measures "did the model stop at the stamp", not a single content fact of the instrument |

### Known-bad readings / refusal phrases (→ `response_not_contains`)

| phrase | status |
|---|---|
| `I cannot` | seeded — current check |
| `unable to read` | seeded — current check |
| TO BE READ BY OPERATOR — other refusal phrasings the model has produced on this page | |

## Hand-back

When the operator has filled the blanks: the `seeded` rows stand as-is (they are
the 2026-08-03 readings, already pseudonymized), the filled rows are the new
checks, and anything the operator strikes out gets a line here saying it is not
on the page. Downstream, the probe edits (corners first) are mechanical:
`response_contains` for the fact rows, `response_not_contains` for the
known-bad rows.
