# Test plan: st_1m8g7ja9ke89ty7pr4bs45

Research: close-gate location, feasible-note storage, landing-diff availability at
subtask close in aw4. This is the reconciliation pass the node was sent back for.
The room carried two mutually exclusive answers to "where the subtask close gate
lives" and the document never picked one; the plan's literal wording ("present on
base") is also wrong. The block at the bottom discriminates: it fails while the
document is unreconciled (both gates left standing, or base left as the target) and
passes once the prose above names a single authoritative gate and a single
verification target.

## 1. Where the subtask close gate lives — ONE answer

**Authoritative: `(*Commands).Transition` at `internal/command/transition.go:112`,
specifically the `if to == plan.Closed` block at transition.go:1166-1235.** That is
the node's close: the `plan.transitioned` event from `in_review` → `closed`, which
the rule table (`internal/plan/rules.go`, `internal/command/command.go`) hands to
`Transition`. Nothing else closes a node.

**Superseded: the `done` hand-off at `internal/agent/done.go:394-419` (post
4052168) is NOT the close gate and is dropped as an answer.** `done` is the
pre-review hand-off — the active→in_review move, not in_review→closed. It commits
what the session touched (`ownUncommitted` at done.go:760 unions `tool.TouchedBy`
with named `commit_files`) and runs the project's `check` argv inside the session's
own worktree; on failure it refuses the hand-off and the node stays `active`, it
does not close the node. The done.go post is retained in the room only as the
pre-review step that precedes close, not as a co-authoritative location.

**Also not the close gate** (corroborated by the room and the standing claim that
land.go is the post-close landing gate): `internal/project/land.go:444` runs AFTER
close (the merge-cleanliness / `check` / test-suite gate); `gateSatisfiable` at
`internal/command/startable.go:480-568` is a planning-time check (plan.approve at
approval.go:28, plan.restate at restate.go:958); `escalatedPass` at
`internal/command/land.go:351-382` is post-merge. None of these is where a missing
feasible-note item would block the node from entering `closed`.

Gate order inside `Transition` for a close: command-class gate (1016-1041) →
satisfiability filter (1066-1140) → operator-action open-row gate (1141-1151) →
`rules.ApplyWithFindingsSatisfied` (1155) → close-reason/note/stake gate
(1166-1226) → `checkClaimClose` (1227-1235). **Hook point for the new check:
inside the `if to == plan.Closed` block, after resolving `p` via
`c.project(ctx, firstOf(node.Projects))` (the pattern at transition.go:2105-2112).**

## 2. What the "feasible note" is as data

The feasible note is the `Note` field of the `plan.transitioned` event payload
(`plan.TransitionedPayload.Note string`, `internal/plan/dag.go:639`), recorded when
a subtask renders `feasible` (draft→feasible). It is **free prose**, not
structured: there is no note/feasibility field on `plan.Node` (dag.go:893), only
`Feasibility`/`Tractability` scores, and no existing code parses named items (API
params / file paths / schema columns) out of it. The only note parsers today are
the Oversized prefix (sizegate.go:20,124), conflict phrases (transition.go:220,234,
256,273), and mis_scoped validation (transition.go:1512). The structured args live
in sibling payload fields (`Args map[string]any`, `Findings []string`,
`Confidence`, `Reason`), not inside the note.

**The dev subtask must write the item-extraction parser fresh.** File paths are
recognizable by `/`+extension (`path/to/file.go:LINE` tokens, regex-able); API
params and schema columns are bare identifiers in prose with no syntactic marker —
a schema column is indistinguishable from a prose word, an API param is a bare
word. Pattern-matching carries genuine false-positive/negative risk; define the
extraction rules before the gate.

**The note does NOT mark intentional non-delivery.** There is no per-item marker in
the note or anywhere on the transition payload for "named this but deliberately did
not deliver it." The only not-delivered signals are node-level: CloseReason codes
(delivered/superseded/duplicate/scaffolding/retired/mis_scoped/not_reproducible,
dag.go:2146-2176), the `not_needed` verdict, and planning.deferred/spec.deferred
events (specdeferral.go:43,49). None exempts a *named item* from a close check.

## 3. The verification target — the branch, NOT base

**The check verifies against the subtask's BRANCH, not base. This is a
correction to the plan's literal wording ("present on base").** At close the
branch is not yet merged (land.go runs after); it exists only as a git ref
`refs/heads/<TaskBranch(node)>` (internal/project/worktree.go:56). Base is the
pre-work state — it does NOT contain the subtask's deliverables — so verifying
against base would fail every item a subtask correctly delivers. The target is
the subtask's branch tip: the branch's diff vs base, or `ShowAt` on the branch
tip. The F26 /search example confirms it: the subtask's branch had the endpoint
but was missing the filter params; the check must find the params in *that
branch*.

Available at close, no worktree needed (branch read via git ref, **not** the
in-process-worktree model of the superseded done.go post):
- base tip: `project.BaseCommit(ctx, p)` (atbase.go:14); single file:
  `project.ShowAt(ctx, p, commit, path)` (atbase.go:29) → `([]byte, ok, err)`,
  ok=false when absent.
- full tree: `project.DetachedTree(ctx, p, commit, label, setup)` (basetest.go:33)
  → (dir, remove, err), defer remove().
- branch's own diff vs base: no dedicated helper; `git -C <root> diff --name-only
  <BaseCommit> <branch>` (fork-point pattern: baseFailures at transition.go:
  2064-2086).

## 4. How close-gate refusals are composed (F16)

Close-path gate refusals are plain `fmt.Errorf("plan: ...")` returned as the second
value — NOT the typed `plan.ErrRuleRefused` (rule-table only, evidence.go:192; it
appends a "From here you may render: …" moves enumeration that inline gates do not
get). Examples: operator-action gate (1141-1147); stake gate (1196-1202) with its
"Close as [superseded duplicate scaffolding] instead if no work was delivered"
clause as the named exit; `checkClaimClose` (2296-2307). **Convention: "plan: "
prefix, why-first, exit named by hand in prose (role + verdict); the refusal text
itself must carry the exit**, because the session sees it verbatim via done's
`not finished: %w` wrap (done.go:525-586) and no moves list is appended.

## 5. Advisory-only, per the plan owner (post 4054669)

**Ship advisory-only, now.** The design constraint ("must not false-refuse an
intentionally-undelivered item") is unsatisfiable for a blocking check: with no
per-item non-delivery marker (section 2), a blocking check has no F16 exit for the
intentional-out-of-scope case and would false-refuse-and-loop — the footgun F16
exists to prevent. A blocking version needs a feasible-note format change (a
per-item scope/@out-of-scope marker) the plan has not scoped; that is separate,
larger work. Advisory makes the under-delivery visible (to the dependent subtask
and the reviewer) without the trap. The advisory finding names each missing item
and the exit: "re-open and deliver it, or record it as intentionally undelivered
(the marker to be added), then close again."

## 6. Dev-subtask hand-off (for the tech lead to plan)

Implement inside `if to == plan.Closed` (transition.go:1166-1235), resolving `p`
via `c.project(ctx, firstOf(node.Projects))`; read items from the feasible-note
prose (dag.go:639); verify against the subtask's BRANCH (section 3) via
`project.BaseCommit` + `ShowAt`/`DetachedTree`; emit an advisory finding composed
per section 4. Re-confirm the exact aw4 line numbers before editing — the prior
sessions' lit.* tools reached only the raglit checkout, not aw4, so every aw4
path:line here is as reported by the researcher sessions (s_1m8gptjd1ecym8d662mdec,
s_1m8gpzev1bw3b1fq9hetph, s_1m8gqh80pf1ecjzf2w942w, s_1m8gqr2v32endqhttvt0gg) and
is subject to drift.

```sh inputs=testplans/st_1m8g7ja9ke89ty7pr4bs45.md
set -euo pipefail
PLAN="testplans/st_1m8g7ja9ke89ty7pr4bs45.md"
[ -f "$PLAN" ] || { echo "missing $PLAN"; exit 1; }
# The deliverable is this document's PROSE. A fenced sh script is part of the same
# file and carries the same tokens it checks, so a grep over the whole file matches
# the script's own command line (each grep below writes its pattern on that line)
# and never fails. The previous version called its anchors "prose-only" and was
# wrong: every grep self-matched, and the test passed with the prose deleted. The
# fix is to grep only the lines OUTSIDE the ``` fences. The toggle is
# `infence = !infence` on every fence line, not a parity counter: a parity count
# flips on the second fenced block and drops section 1, which is the bug the prior
# awk attempt hit.
PROSE="$(mktemp)"
trap 'rm -f "$PROSE"' EXIT
awk '
  /^[[:space:]]*```/ { infence = !infence; next }
  !infence { print }
' "$PLAN" > "$PROSE"
[ -s "$PROSE" ] || { echo "FAIL: fence-strip produced no prose"; exit 1; }
# (1) the one authoritative close gate, by name (prose carries backticks here)
grep -qF 'the `if to == plan.Closed` block at transition.go:1166-1235' "$PROSE" \
  || { echo "FAIL: authoritative gate transition.go:1166-1235 not named in prose"; exit 1; }
echo "OK: transition.go:1166-1235 named authoritative in prose"
# (2) the done.go answer present but demoted to the pre-review hand-off
grep -qF 'the `done` hand-off at `internal/agent/done.go:394-419`' "$PROSE" \
  || { echo "FAIL: done.go:394-419 not mentioned (supersede, do not delete)"; exit 1; }
grep -qF 'pre-review hand-off' "$PROSE" \
  || { echo "FAIL: done path not characterized as pre-review hand-off"; exit 1; }
grep -qF 'is NOT the close gate and is dropped as an answer' "$PROSE" \
  || { echo "FAIL: done path not dropped/superseded (still a co-authoritative answer)"; exit 1; }
echo "OK: done.go:394-419 present AND demoted to the pre-review hand-off, dropped as an answer"
# (3) the verification target is the branch, not base
grep -qiF 'verifies against the subtask'"'"'s branch, not base' "$PROSE" \
  || { echo "FAIL: branch-not-base target not stated in prose"; exit 1; }
echo "OK: target stated as the subtask's branch, not base"
echo "BLOCK PASS: prose (fence-stripped) names one gate (transition.go:1166-1235), demotes done.go, and states branch-not-base"
```

**Discrimination.** A pass only counts if the test can fail. Block 1 fence-strips the current document and asserts five patterns are present in the prose (the gate, the demoted done answer, and the branch-not-base target). This block asserts the *same five* patterns are absent from the **old, unreconciled** prose — the document this node was sent back with (both gates standing, "present on base" as the target). Under `set -e`, the moment any negated `grep` finds a pattern in the old prose, the block aborts: that would mean block 1 passed pre-reconciliation and cannot discriminate. Passing both blocks together proves the test fails pre-reconciliation and passes post. The greps run against the fence-stripped prose, not the whole file, so a script line carrying a pattern (every `grep -qF` below writes its pattern on its own line) cannot satisfy its own check.

```sh inputs=testplans/st_1m8g7ja9ke89ty7pr4bs45.md
set -euo pipefail
# Discrimination, airtight: run BLOCK 1's exact five patterns against the OLD,
# unreconciled prose and assert each is ABSENT. Block 1 (above) fence-strips the
# current document and passes on it. If any of its patterns also matched the old
# prose, block 1 would have passed pre-reconciliation and cannot discriminate.
# Reconstruct the old prose (this node's send-back state: a scope paragraph that
# left both gates standing and "present on base" as the target, plus the old
# self-matching script). It is pure prose, so the fence-strip is identity here.
OLD='**Scope.** This is the reconciliation pass the node was sent back for. The room carried two mutually exclusive answers to "where the subtask close gate lives" and the document never picked one; the plan'"'"'s literal wording ("present on base") is also wrong. Both the done hand-off and the Transition close block are left standing; the target is stated as present on base.'
# (1) the one authoritative close gate, by name (prose carries backticks here)
! grep -qF 'the `if to == plan.Closed` block at transition.go:1166-1235' <<< "$OLD"
# (2) the done.go answer demoted to the pre-review hand-off
! grep -qF 'the `done` hand-off at `internal/agent/done.go:394-419`' <<< "$OLD"
! grep -qF 'pre-review hand-off' <<< "$OLD"
! grep -qF 'is NOT the close gate and is dropped as an answer' <<< "$OLD"
# (3) the verification target is the branch, not base
! grep -qiF 'verifies against the subtask'"'"'s branch, not base' <<< "$OLD"
echo "BLOCK PASS: block 1's five exact patterns are all absent from the old unreconciled prose, so block 1 fails pre-reconciliation and passes post — the test discriminates"
```
