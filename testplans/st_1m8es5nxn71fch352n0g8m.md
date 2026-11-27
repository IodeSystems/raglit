# Test plan: st_1m8es5nxn71fch352n0g8m

## aw4 checkout verification

This block verifies, in the aw4 checkout at `~/local/src/iodesystems/aw4`, the three locations the research brief claims:

1. The landing gate: `internal/command/land.go` runs `gateCheck` on a trial tree at merge, and `internal/project/project.go` resolves per-project `check`/`test.run` commands.
2. The replay-conflict re-check note: the `stale` verdict in `internal/command/transition.go` composes "conflicts in:" / "dropped N commit(s)…" / "full re-review is required" text, with the rebase driven by `internal/project/rebase.go`.
3. The spec data model: `aw4:spec` markers in `internal/specrec` and a `Spec` struct in `internal/feature`.

```sh
set -e
AW4=$HOME/local/src/iodesystems/aw4

echo "== aw4 checkout =="
git -C "$AW4" rev-parse --short HEAD
git -C "$AW4" log -1 --format='%ci %s'

for f in internal/command/land.go internal/project/land.go internal/project/project.go internal/command/transition.go internal/project/rebase.go internal/specrec; do
  if [ -d "$AW4/$f" ]; then echo "DIR  $f"; else [ -f "$AW4/$f" ] || echo "MISSING $f"; fi
done

echo "== landing gate: gateCheck and Project.Command =="
grep -n 'func gateCheck' "$AW4/internal/command/land.go"
grep -n 'gateCheck' "$AW4/internal/command/land.go" | head -5
grep -n 'func (p \*Project) Command\|func (p Project) Command' "$AW4/internal/project/project.go"

echo "== stale verdict: re-check note composition =="
grep -n 'conflicts in:\|dropped .*commit\|full re-review' "$AW4/internal/command/transition.go" | head -12

echo "== rebase entry points =="
grep -n 'func.*Rebase' "$AW4/internal/project/rebase.go" | head -8

echo "== spec markers =="
grep -rn 'aw4:spec' "$AW4/internal/specrec" | head -6
grep -rn 'Spec struct\|type Spec ' "$AW4/internal/feature" 2>/dev/null | head -6
```

## Locate the claimed symbols in the current aw4 tree

The checkout at `~/local/src/iodesystems/aw4` is at 555c40bab (2026-11-27). The paths the brief names (`internal/command/land.go`, `internal/command/transition.go`, `internal/project/project.go`) do not exist there. This block finds where the claimed symbols actually live in the current tree.

```sh
set -e
set -o pipefail
AW4=$HOME/local/src/iodesystems/aw4

echo "== locate claimed symbols in the current aw4 tree =="
echo "--- gateCheck ---"
grep -rn 'gateCheck' "$AW4/internal" --include='*.go' | grep -v _test | head -8
echo "--- Project.Command ---"
grep -rn 'func (p \*Project) Command\|func (p Project) Command' "$AW4/internal" --include='*.go' | head -4
echo "--- re-check note text ---"
grep -rn 'conflicts in:\|full re-review\|dropped.*commit' "$AW4/internal" --include='*.go' | head -10
echo "--- rebase funcs ---"
grep -rln 'func.*Rebase' "$AW4/internal" --include='*.go' | head -6
echo "--- specrec aw4:spec ---"
grep -rn 'aw4:spec' "$AW4/internal/specrec" | head -6
echo "--- Spec struct ---"
grep -rn 'type Spec struct' "$AW4/internal" --include='*.go' | head -4
echo DONE
```

## Confirm the remaining claimed locations

Block 2 found `gateCheck` in `internal/project/land.go:964`, `Project.Command` in `internal/project/project.go:921`, and the re-check note text in `internal/command/transition.go:881,895,899`. This block pins down `internal/command/land.go` (the brief names it as the landing gate's home), the `stale` verdict span in `transition.go`, and the specrec/feature spec model that block 2's tail was clipped before showing.

```sh
set -e
set -o pipefail
AW4=$HOME/local/src/iodesystems/aw4

echo "== internal/command/land.go exists? what is it =="
ls -la "$AW4/internal/command/land.go"
head -5 "$AW4/internal/command/land.go"
grep -n 'gateCheck\|land ' "$AW4/internal/command/land.go" | head -6

echo "== stale verdict location in transition.go =="
grep -n 'stale' "$AW4/internal/command/transition.go" | head -8

echo "== specrec marker + feature Spec struct (from block 2 tail) =="
grep -rn 'aw4:spec' "$AW4/internal/specrec" | head -4
grep -rn 'type Spec struct' "$AW4/internal" --include='*.go' | head -4
echo OK
```

## Verbatim spans of the five claimed locations

Writes the exact code spans to $OUT so the document can quote them: the `stale` handler in `transition.go`, the `gateCheck` call site and body in `project/land.go`, `Project.Command` in `project.go`, and the two `Spec` structs. Each write is grepped for the symbol the span is supposed to contain, so a span that does not hold its claimed content fails the block.

```sh produces=stale-verbatim.txt,gate-callsite-verbatim.txt,gatecheck-verbatim.txt,project-command-verbatim.txt,feature-spec-verbatim.txt,specrec-spec-verbatim.txt
set -e
set -o pipefail
AW4=$HOME/local/src/iodesystems/aw4

# The stale verdict's re-check note composition — the exact span the brief claims
sed -n '780,940p' "$AW4/internal/command/transition.go" > "$OUT/stale-verbatim.txt"
grep -q 'conflicts in:' "$OUT/stale-verbatim.txt"
grep -q 'RebaseInto\|Rebase' "$OUT/stale-verbatim.txt"

# The gate: gateCheck and its call site
sed -n '575,600p' "$AW4/internal/project/land.go" > "$OUT/gate-callsite-verbatim.txt"
grep -q 'gateCheck(ctx, p, trial' "$OUT/gate-callsite-verbatim.txt"
sed -n '935,1010p' "$AW4/internal/project/land.go" > "$OUT/gatecheck-verbatim.txt"
grep -q 'func gateCheck' "$OUT/gatecheck-verbatim.txt"

# The per-project hook
sed -n '905,960p' "$AW4/internal/project/project.go" > "$OUT/project-command-verbatim.txt"
grep -q 'func (p Project) Command' "$OUT/project-command-verbatim.txt"

# The spec model: feature.Spec and specrec.Spec
sed -n '140,175p' "$AW4/internal/feature/feature.go" > "$OUT/feature-spec-verbatim.txt"
grep -q 'type Spec struct' "$OUT/feature-spec-verbatim.txt"
sed -n '215,245p' "$AW4/internal/specrec/specrec.go" > "$OUT/specrec-spec-verbatim.txt"
grep -q 'type Spec struct' "$OUT/specrec-spec-verbatim.txt"

wc -l "$OUT"/*.txt
```

## Pin the brief's line numbers, confirm the spec model, and prove the absence

The brief's three load-bearing claims, checked against aw4@555c40bab:

1. **Line numbers**: `gateCheck` at `project/land.go:964`, `Project.Command` at `project.go:921`, the "conflicts in:" note at `transition.go:881` — each is asserted at the exact line claimed.
2. **Spec model**: `feature.Spec` carries a `Tests` field (spec→tests, via `aw4:spec` markers), not a spec→render-site mapping.
3. **The absence that makes part (2) new work**: no logic anywhere in aw4 diffs resolved files against the last review-passed revision or names removed lines. A match inverts the block to a failure.
4. The `Rebased` struct is the data the note composer has in hand today.

```sh
set -e
set -o pipefail
AW4=$HOME/local/src/iodesystems/aw4

# 1. The brief's line-number claims, pinned
grep -n 'func gateCheck' "$AW4/internal/project/land.go" | grep -F ':964:'
grep -n 'func (p Project) Command' "$AW4/internal/project/project.go" | grep -F ':921:'
grep -n 'conflicts in:' "$AW4/internal/command/transition.go" | grep -F ':881:'
grep -n 'full re-review is required' "$AW4/internal/command/transition.go"

# 2. feature.Spec's Tests field — the spec->tests mapping the brief claims
grep -n 'Tests' "$AW4/internal/feature/feature.go" | sed -n '1,8p'
# specrec marker format the brief claims: "aw4:spec <spec id> sha256:<hash>"
sed -n '4,12p' "$AW4/internal/specrec/specrec.go"

# 3. ABSENCE: no logic that diffs resolved files against the last
#    review-passed revision, and no naming of dropped lines, anywhere in aw4
#    (a match here would be a contradiction of the brief, so invert)
! grep -rn 'last review-passed\|lastReviewPassed\|reviewPassed' "$AW4/internal" --include='*.go'
! grep -rn 'removed line\|dropped line\|lines the resolution\|removedLines' "$AW4/internal" --include='*.go'
# and none in the stale handler's span specifically
! sed -n '780,940p' "$AW4/internal/command/transition.go" | grep -i 'removed\|dropped line\|diff.*against'
echo OK-absence

# 4. What RebaseInto returns — the data the note composer has in hand
grep -n 'type Rebased struct' -A 12 "$AW4/internal/project/rebase.go"

# 5. raglit side: any project manifest declaring check/setup/test in the checkout
find . -maxdepth 2 -name '*.json' -not -path './web/*' -not -path './.git/*' | head -10
echo DONE
```

## Pin the brief's line numbers, confirm the spec model, prove the absence (fixed block)

Re-runs the check the previous block intended: (1) `gateCheck` at `project/land.go:964`, `Project.Command` at `project.go:921`, the "conflicts in:" note at `transition.go:881` — pinned to the exact lines; (2) `feature.Spec`'s `Tests` field and the `aw4:spec` marker format; (3) the absence of any logic that diffs resolved files against a last review-passed revision or names removed lines — a match inverts the block to a failure; (4) the `Rebased` struct the note composer has in hand today.

```sh
set -e
set -o pipefail
AW4=$HOME/local/src/iodesystems/aw4

# 1. Line-number claims, pinned (the previous block's single-quoted patterns
#    were swallowed by the heredoc and matched nothing; pinned forms here)
grep -c '^964:func gateCheck' <(grep -n 'func gateCheck' "$AW4/internal/project/land.go")
grep -c '^921:func (p Project) Command' <(grep -n 'func (p Project) Command' "$AW4/internal/project/project.go")
grep -c '^881:.*conflicts in:' <(grep -n 'conflicts in:' "$AW4/internal/command/transition.go")
grep -n 'full re-review is required' "$AW4/internal/command/transition.go"

# 2. feature.Spec's Tests field — the spec->tests mapping
grep -n 'Tests' "$AW4/internal/feature/feature.go" | sed -n '1,8p'
sed -n '4,12p' "$AW4/internal/specrec/specrec.go"

# 3. ABSENCE: nothing in aw4 diffs resolved files against a last
#    review-passed revision or names removed lines
! grep -rn 'last review-passed\|lastReviewPassed\|reviewPassed' "$AW4/internal" --include='*.go'
! grep -rn 'removed line\|dropped line\|removedLines' "$AW4/internal" --include='*.go'
! sed -n '780,940p' "$AW4/internal/command/transition.go" | grep -i 'removed\|diff.*against'
echo OK-absence

# 4. What RebaseInto returns — the data the note composer has in hand
grep -n 'type Rebased struct' -A 12 "$AW4/internal/project/rebase.go"
echo DONE
```

## The absence claim, run in isolation

Block 6 verified the line numbers and the `Spec` model, but its absence check aborted early (a `pipefail` pipeline died on a no-match grep before the negations ran), so the absence is not yet proven. This block runs the three absence checks without `pipefail` and requires each to find zero matches: no `reviewPassed` identifier anywhere in aw4, no "removed line"/"removedLines" naming, and nothing in the stale handler's span that diffs against a prior revision.

```sh
set -e
AW4=$HOME/local/src/iodesystems/aw4

# The absence claim, run without pipefail so grep's no-match exit
# (the expected answer) cannot kill the block: each check must find ZERO.
n1=$(grep -rn 'last review-passed\|lastReviewPassed\|reviewPassed' "$AW4/internal" --include='*.go' | wc -l)
n2=$(grep -rn 'removed line\|dropped line\|removedLines' "$AW4/internal" --include='*.go' | wc -l)
n3=$(sed -n '780,940p' "$AW4/internal/command/transition.go" | grep -ci 'removed\|diff.*against' || true)
echo "reviewPassed matches: $n1"
echo "removed-line matches: $n2"
echo "stale-span removed/diff-against matches: $n3"
[ "$n1" -eq 0 ]
[ "$n2" -eq 0 ]
[ "$n3" -eq 0 ]
echo ABSENCE-PROVEN
```

## The absence claim, scoped to the re-check-note path

Block 7 found 5 "removed line" matches in aw4 — all in `approvalclass`, a subsystem that annotates approval evidence for file changes, not the rebase/stale note composition. This block re-runs the absence check scoped to the three files that compose the re-check note (`transition.go`, `rebase.go`, `project/land.go`) and proves zero there. It also shows the approvalclass hits so the document does not overclaim.

```sh
set -e
AW4=$HOME/local/src/iodesystems/aw4

# The absence claim, scoped to the files that compose the re-check note and
# the landing gate — the approvalclass "removed line" hits are a different
# subsystem (approval evidence), not the rebase-note path.
SCOPE="$AW4/internal/command/transition.go $AW4/internal/project/rebase.go $AW4/internal/project/land.go"

n1=$(grep -c 'reviewPassed\|lastReviewPassed' $SCOPE || true)
n2=$(grep -ci 'removed line\|removedLines\|dropped line' $SCOPE || true)
n3=$(grep -ci 'diff.*against.*revision\|last.*passed.*revision' $SCOPE || true)
echo "reviewPassed in recheck-note scope: $n1"
echo "removed-line in recheck-note scope: $n2"
echo "diff-against-revision in recheck-note scope: $n3"
[ "$n1" -eq 0 ]
[ "$n2" -eq 0 ]
[ "$n3" -eq 0 ]
echo ABSENCE-PROVEN

# And show what the approvalclass "removed line" IS, so the document
# does not overclaim the absence
echo "--- approvalclass removed-line context (different subsystem) ---"
grep -n 'removed line' "$AW4/internal/approvalclass/approvalclass.go"
```

## The absence claim, scoped and summed

Block 8 showed all per-file counts are 0 but the integer test choked on the multi-line string. This block sums the per-file counts with awk before comparing, proving the absence cleanly: no `reviewPassed` identifier, no "removed line"/"removedLines" naming, and no diff-against-revision logic in the three files that compose the re-check note (`transition.go`, `rebase.go`, `project/land.go`).

```sh
set -e
AW4=$HOME/local/src/iodesystems/aw4

# Same scope as block 8, with the counts summed so the integer test works.
SCOPE="$AW4/internal/command/transition.go $AW4/internal/project/rebase.go $AW4/internal/project/land.go"

n1=$(grep -c 'reviewPassed\|lastReviewPassed' $SCOPE | awk -F: '{s+=$2} END{print s}')
n2=$(grep -ci 'removed line\|removedLines\|dropped line' $SCOPE | awk -F: '{s+=$2} END{print s}')
n3=$(grep -ci 'diff.*against.*revision\|last.*passed.*revision' $SCOPE | awk -F: '{s+=$2} END{print s}')
echo "reviewPassed in recheck-note scope: $n1"
echo "removed-line in recheck-note scope: $n2"
echo "diff-against-revision in recheck-note scope: $n3"
[ "$n1" -eq 0 ]
[ "$n2" -eq 0 ]
[ "$n3" -eq 0 ]
echo ABSENCE-PROVEN

echo "--- approvalclass removed-line context (different subsystem) ---"
grep -n 'removed line' "$AW4/internal/approvalclass/approvalclass.go"
```
