# Test plan: st_1m7x696rv01hwqa3cdmyb1

Spike: produce the operator's ground-truth sheet for the three saturated OCR
fixtures (`ocr-survey-corners`, `ocr-drawing-dimensions`, `ocr-scanned-exhibit`).
The artifact is `testplans/st_1m7x696rv01hwqa3cdmyb1-ground-truth.md`: a table
per fixture of (a) exact facts a person read, seeded from what
`docs/design/hierarchical-regions.md` (2026-08-03) and
`docs/design/ocr-fixtures.md` (2026-08-03) already record, and (b) known-bad
readings a model has actually produced, ready for the operator to confirm and
complete.

## Block 1 — the artifact exists, is complete, and is ready to hand over

The sheet must name all three fixtures, carry the seeded truths and
known-bad entries, and mark every gap for the operator explicitly.

```sh produces=artifact-check.log inputs=testplans/st_1m7x696rv01hwqa3cdmyb1-ground-truth.md
set -e
A=testplans/st_1m7x696rv01hwqa3cdmyb1-ground-truth.md
{
  for s in ocr-survey-corners ocr-drawing-dimensions ocr-scanned-exhibit
  do grep -q "$s" "$A" || { echo "MISSING fixture section: $s"; exit 1; }; done
  for t in "S 31°05' E 0.4' FROM CALC" "S 47°44'E 2.0' FROM CORNER" "0.1'S AND 0.1'W OF CALC" "MOWRER" "DACE 20123169"
  do grep -qF "$t" "$A" || { echo "MISSING seeded truth: $t"; exit 1; }; done
  for b in "5.31' 0.5' E 0.4' FROM CALC" "5.47' 0.44' E 2.0' FROM CORNER" "0.15' AND 0.14' W OF CALC"
  do grep -qF "$b" "$A" || { echo "MISSING known-bad: $b"; exit 1; }; done
  for t in 196.53 224.6 203.1 196.23
  do grep -qF "$t" "$A" || { echo "MISSING drawing dimension: $t"; exit 1; }; done
  for t in "Authentisign" "I cannot" "unable to read"
  do grep -qF "$t" "$A" || { echo "MISSING exhibit check: $t"; exit 1; }; done
  n=$(grep -cF 'TO BE READ BY OPERATOR' "$A")
  echo "operator-blank markers: $n"
  [ "$n" -ge 3 ] || { echo "too few operator blanks: $n"; exit 1; }
  echo "artifact checks: pass"
} > "$OUT/artifact-check.log" 2>&1
cat "$OUT/artifact-check.log"
```

## Block 2 — the new tracked file passes the leak guard

`TestNoRealMatterDetail` scans every tracked file; the sheet is tracked, so it
must pass it.

```sh produces=noleak.log inputs=noleak_test.go,testplans/st_1m7x696rv01hwqa3cdmyb1-ground-truth.md
set -e
{
  go test -run 'TestNoRealMatterDetail' . | tail -3
} > "$OUT/noleak.log" 2>&1
cat "$OUT/noleak.log"
```
