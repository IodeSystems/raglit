# Test plan: bench/agent-ab ledger infra (st_1m8gg00h7avqx6vhv4dmmz)

Ledger behavior is re-derived from the plan's spec (the killed session's test
plan document is gone with its discarded branch). The synthetic out dir is a
corpus of 321 files: the 28 committed corpus files plus 293 generated
distractor files, so `corpus_files` is exactly 321 and the ratio arithmetic is
clean.

The rescore path needs no model and no embeddings endpoint: it reads
`OUT/results.jsonl` and `OUT/meta.json` and re-scores answers against
`questions.json`, so every block below is runnable offline.

## Block 1: over-bound rescore exits nonzero and appends one ledger row

A synthetic out dir with a 321-file corpus (the committed corpus files plus
300 generated distractors, 321 total) and a 2.2x raglit-to-grep mean
tool_result_chars ratio, rescored through `--rescore`, must exit nonzero with
the "over the 2.0x bound" message and append exactly one row to
`bench/agent-ab/ledger.jsonl` carrying the per-arm means, the ratio and
`corpus_files: 321`.

```sh inputs=bench/agent-ab/run.py,bench/agent-ab/gen-distractors.py,bench/agent-ab/questions.json
set -u
cd "$(git rev-parse --show-toplevel)"
OUT=$(mktemp -d)
# clean ledger so this run's row count is exact
rm -f bench/agent-ab/ledger.jsonl
# synthetic 321-file corpus: the 28 committed files + 293 distractors
cp -r bench/agent-ab/corpus "$OUT/corpus"
python3 bench/agent-ab/gen-distractors.py "$OUT/corpus" -n 293 >/dev/null
nfiles=$(find "$OUT/corpus" -type f | wc -l)
echo "corpus files: $nfiles"
[ "$nfiles" = "321" ] || { echo "FAIL: expected 321 files, got $nfiles"; exit 1; }
python3 - "$OUT" "$nfiles" <<'EOF'
import json, sys
from pathlib import Path
out = Path(sys.argv[1]); nfiles = int(sys.argv[2])
probes = {"raglit": 1200, "grep": 400}
meta = {"probes": probes, "nfiles": nfiles, "nbytes": 900000,
        "model": "haiku", "reps": 3, "filler": 300, "embed_model": "nomic-embed-text"}
(out / "meta.json").write_text(json.dumps(meta))
rows = []
for arm, chars in (("raglit", 22000), ("grep", 10000)):
    for rep in range(3):
        rows.append({"arm": arm, "id": "vesting", "rep": rep, "ok": True, "error": None,
                     "answer": "You have earned 50% of the employer match after two years.",
                     "input_tokens": 5000, "output_tokens": 80, "turns": 3,
                     "tool_calls": {"Read": 1}, "tool_result_chars": chars,
                     "cost_usd": 0.02, "wall_s": 12.0})
with open(out / "results.jsonl", "w") as fh:
    for r in rows:
        fh.write(json.dumps(r) + "\n")
EOF
rc=0
python3 bench/agent-ab/run.py --rescore --out "$OUT" > "$OUT/rescore.log" 2>&1 || rc=$?
echo "exit: $rc"
tail -5 "$OUT/rescore.log"
grep -q "over the 2.0x bound" "$OUT/rescore.log" || { echo "FAIL: no bound message"; exit 1; }
[ "$rc" -ne 0 ] || { echo "FAIL: over-bound rescore exited 0"; exit 1; }
nrows=$(wc -l < bench/agent-ab/ledger.jsonl)
[ "$nrows" = "1" ] || { echo "FAIL: expected 1 ledger row, got $nrows"; exit 1; }
cat bench/agent-ab/ledger.jsonl
python3 - bench/agent-ab/ledger.jsonl <<'EOF'
import json, sys
row = json.loads(open(sys.argv[1]).readline())
assert row["corpus_files"] == 321, row
assert row["mean_tool_result_chars"] == {"grep": 10000.0, "raglit": 22000.0}, row
assert abs(row["ratio"] - 2.2) < 1e-6, row
print("row ok")
EOF
```

## Block 2: under-bound rescore exits zero; a second rescore is not re-recorded

The same synthetic out dir at a 1.6x ratio must exit zero (no bound
message). Rescoring it a second time must print "already recorded" and
append no second ledger row for the same run.

```sh inputs=bench/agent-ab/run.py,bench/agent-ab/gen-distractors.py
set -eu
cd "$(git rev-parse --show-toplevel)"
OUT=$(mktemp -d)
cp -r bench/agent-ab/corpus "$OUT/corpus"
python3 bench/agent-ab/gen-distractors.py "$OUT/corpus" -n 293 >/dev/null
nfiles=$(find "$OUT/corpus" -type f | wc -l)
echo "corpus files: $nfiles"
[ "$nfiles" = "321" ] || { echo "FAIL: expected 321 files, got $nfiles"; exit 1; }
python3 - "$OUT" "$nfiles" <<'EOF'
import json, sys
from pathlib import Path
out = Path(sys.argv[1]); nfiles = int(sys.argv[2])
meta = {"probes": {"raglit": 1200, "grep": 400}, "nfiles": nfiles, "nbytes": 900000,
        "model": "haiku", "reps": 3, "filler": 300, "embed_model": "nomic-embed-text"}
(out / "meta.json").write_text(json.dumps(meta))
rows = []
for arm, chars in (("raglit", 16000), ("grep", 10000)):
    for rep in range(3):
        rows.append({"arm": arm, "id": "vesting", "rep": rep, "ok": True, "error": None,
                     "answer": "You have earned 50% of the employer match after two years.",
                     "input_tokens": 5000, "output_tokens": 80, "turns": 3,
                     "tool_calls": {"Read": 1}, "tool_result_chars": chars,
                     "cost_usd": 0.02, "wall_s": 12.0})
with open(out / "results.jsonl", "w") as fh:
    for r in rows:
        fh.write(json.dumps(r) + "\n")
EOF
before=$(wc -l < bench/agent-ab/ledger.jsonl)
rc=0
python3 bench/agent-ab/run.py --rescore --out "$OUT" > "$OUT/rescore.log" 2>&1 || rc=$?
echo "exit: $rc"
[ "$rc" -eq 0 ] || { echo "FAIL: under-bound rescore exited $rc"; exit 1; }
grep -q "over the 2.0x bound" "$OUT/rescore.log" && { echo "FAIL: bound message in under-bound run"; exit 1; }
echo "under-bound rescore exits 0, no bound message"
if grep -q "already recorded" "$OUT/rescore.log"; then
  after=$(wc -l < bench/agent-ab/ledger.jsonl)
  [ "$after" -eq "$before" ] || { echo "FAIL: ledger grew $before -> $after"; exit 1; }
  echo "duplicate guard: no second row"
else
  echo "first under-bound run appended its row; rescore again for the duplicate check"
  rc=0
  python3 bench/agent-ab/run.py --rescore --out "$OUT" > "$OUT/rescore2.log" 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || { echo "FAIL: second rescore exited $rc"; exit 1; }
  grep -q "already recorded" "$OUT/rescore2.log" || { echo "FAIL: no already-recorded notice"; exit 1; }
  after=$(wc -l < bench/agent-ab/ledger.jsonl)
  [ "$after" -eq "$((before + 1))" ] || { echo "FAIL: ledger grew $before -> $after, expected +1"; exit 1; }
  echo "duplicate guard: exactly one row for this run"
fi
exit 0
```

## Block 3: `--no-ledger` suppresses the ledger row

An over-bound rescore with `--no-ledger` must still exit nonzero with the
bound message, but must not append a row to `ledger.jsonl`.

```sh inputs=bench/agent-ab/run.py,bench/agent-ab/gen-distractors.py
set -eu
cd "$(git rev-parse --show-toplevel)"
OUT=$(mktemp -d)
cp -r bench/agent-ab/corpus "$OUT/corpus"
python3 bench/agent-ab/gen-distractors.py "$OUT/corpus" -n 293 >/dev/null
nfiles=$(find "$OUT/corpus" -type f | wc -l)
echo "corpus files: $nfiles"
[ "$nfiles" = "321" ] || { echo "FAIL: expected 321 files, got $nfiles"; exit 1; }
python3 - "$OUT" "$nfiles" <<'EOF'
import json, sys
from pathlib import Path
out = Path(sys.argv[1]); nfiles = int(sys.argv[2])
meta = {"probes": {"raglit": 1200, "grep": 400}, "nfiles": nfiles, "nbytes": 900000,
        "model": "haiku", "reps": 3, "filler": 300, "embed_model": "nomic-embed-text"}
(out / "meta.json").write_text(json.dumps(meta))
rows = []
for arm, chars in (("raglit", 22000), ("grep", 10000)):
    for rep in range(3):
        rows.append({"arm": arm, "id": "vesting", "rep": rep, "ok": True, "error": None,
                     "answer": "You have earned 50% of the employer match after two years.",
                     "input_tokens": 5000, "output_tokens": 80, "turns": 3,
                     "tool_calls": {"Read": 1}, "tool_result_chars": chars,
                     "cost_usd": 0.02, "wall_s": 12.0})
with open(out / "results.jsonl", "w") as fh:
    for r in rows:
        fh.write(json.dumps(r) + "\n")
EOF
before=$(wc -l < bench/agent-ab/ledger.jsonl)
rc=0
python3 bench/agent-ab/run.py --rescore --no-ledger --out "$OUT" > "$OUT/rescore.log" 2>&1 || rc=$?
echo "exit: $rc"
# over-bound run with --no-ledger: the bound message is still printed, but no row is appended
grep -q "over the 2.0x bound" "$OUT/rescore.log" || { echo "FAIL: no bound message"; exit 1; }
[ "$rc" -ne 0 ] || { echo "FAIL: over-bound rescore with --no-ledger exited 0"; exit 1; }
after=$(wc -l < bench/agent-ab/ledger.jsonl)
[ "$after" -eq "$before" ] || { echo "FAIL: --no-ledger appended a row ($before -> $after)"; exit 1; }
echo "--no-ledger: bound message printed, no row appended"
exit 0
```
