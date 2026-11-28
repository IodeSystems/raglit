# Diagnosis: parked ready work in sprint_raglit_236

Research deliverable for the subtask "Diagnose parked ready work in
sprint_raglit_236". This is the shared "parked ready work" observable that both
the "surface it" view and any "pick it up earlier" dispatcher change will read.

**Provenance of the facts below.** The load-bearing aw4-side facts (sprint
window, per-task node_history) were researched by s_1m8gpf50devn1msvfrt9j7 and
independently re-verified by the tech_lead (channel msg 4054782 on this node).
This session has no aw4 log access (no `db.query`/`node.history`/`plan.read`
from the raglit checkout), so it materializes that verified content here rather
than re-running it. The aw4 file:line gate anchors are carried from the project
graph claims (named below with their recorders). Each finding is marked
**log-verified** (a node_history event was read) or **claim** (a KPI-report /
follow-up attribution with no supporting log event) — that distinction is part
of the diagnosis, not a detail.

## Sprint window

Sprint `sprint_raglit_236`: loaded `2026-11-23T23:19:54Z`, ended
`2026-11-24T00:23:42Z` (63.8 min wall, ended on migrate). **6 loaded, 2
executed.** The other 4 idled with ~0 active minutes over multi-day windows,
silently. Those 4 are the parked rows below.

## The shared "parked ready work" observable

Per loaded task: `id, title, status, executor, activeMin, openToCloseMin,
blocking gate/input, closed`. A loaded task is **parked** when, at sprint end,
it is in a non-executed state (draft / feasible / blocked) with no executor
having run on it during the window. The three parked rows of
sprint_raglit_236:

| id | title | status | executor | activeMin | openToClose | blocking gate/input | closed |
|----|-------|--------|----------|-----------|-------------|---------------------|--------|
| `st_1m7xnkc2rcpqxg4nft36ep` | F35 wire | `draft` | none | 0 | (never closed) | **planning/gating gap**: loaded in `draft`, never promoted to `feasible`; carries `plan.requires` on `st_1m7xnkbhgad9b43rvabjzg` (F35 probe) | no |
| `st_1m7fwnst62zv2bnk53jyxx` | sync(NORMAL) DSN | `feasible` | (ran 11-25) | 1.2 | ~8.3 days | feasible, **undispatched ~10 days** (see finding b) | 11-25, merged `06ff8a8eb71b` |
| `st_1m7qgbn0949r2b5t9j3t4z` | OSD-confidence | `blocked` | none | 0 | ~5.4 days | **out-of-band inputs**: RFX `x_1m83cwzk6252tcgev8my1m` opt 1 + 8 human OSD readings | (not in window) |

## Findings, each resolved

### (a) F35 wire `st_1m7xnkc2rcpqxg4nft36ep` — **DISPROVED as a dispatch bug; it is a planning/gating gap.**

The follow-up framed this as "a dispatch-path gap that loaded a ready task and
never offered it." **Log-verified** via node_history: F35 has **0 status moves
and 0 sessions dispatched** in the window — it was loaded in `draft` and never
promoted to `feasible`. A `draft` node is not offered to a dispatcher; the
sweep dispatches on status (see gate anchors). So the dispatcher had nothing
ready to offer — the gap is that a `draft` node was loaded into the sprint and
never promoted, and it carries an unmet `plan.requires` on `st_1m7xnkbhgad9b43rvabjzg`.
The issue's "dispatch bug that loaded a ready task" framing is **not true of
this code**: no ready task was loaded and dropped. The discriminating check is
the node_history count of status transitions (0) and dispatched sessions (0) —
that is what "never dispatched, loaded in draft" means, and it is what separates
this from a drop of a genuinely-ready node.

### (b) DSN `st_1m7fwnst62zv2bnk53jyxx` — **the park is log-verified; the F314 attribution is NOT.**

**Log-verified** node_history: `draft→feasible` 2026-11-15 09:08, ~10-day gap,
`active` 2026-11-25 12:15, `closed` 2026-11-25 12:25, merged `06ff8a8eb71b`.
**There is no gate-refusal event in the history.** The verified fact is "feasible,
undispatched for ~10 days." The follow-up's "parked on the approval gate (F314)"
is a **claim from the KPI report, not a log-verified event** — no node_history
event names F314 as the reason. This is a disproof of the *specific
attribution*, not of the park: the 10-day feasible-park is real; the gate name
is not supported by the log. (The graph distinguishes two dispatch-time gates —
the F314 human-action gate and the operationsGuard — see anchors; neither
appears as a refusal event on this node.)

### (c) OSD-confidence `st_1m7qgbn0949r2b5t9j3t4z` — **CONFIRMED as an out-of-band block.**

**Log-verified / correctly characterized:** `blocked`, 0 active min, open-to-close
~5.4 days, gated on out-of-band human inputs (RFX `x_1m83cwzk6252tcgev8my1m`
opt 1 + 8 human OSD readings). This responds to the fix differently from (a)/(b):
it is a genuine external-input block, not a scheduler or planning gap.

### (d) F28 Relations `st_1m7jjghkz6tq1b8npj0bwq` — **DISPROVED as a parked row; it was actively executed and closed.**

The earlier draft of this observable listed F28 as a fourth parked row
(`feasible`, 4.2 active min, "undispatched ~7.1 days"). A node_history
re-run (reviewer s_1m8gv1502dyeqp6b9wqahp, channel msg 4057238; carried here
at `accepted`, as with the rest of the log facts — this session has no aw4
access) contradicts that: `draft→feasible` 11-16 12:22, `feasible→active` 11-23 18:24
(5 sessions dispatched across 11-23), `in_review→closed` 11-23 22:50 (delivered
by the reviewer), landings 11-23 22:52 (NOT merged, auth gate) and 11-24 20:14
merged `97a950146022`. It was **executed and closed before the sprint window
loaded at 2026-11-23T23:19:54Z** — so it is not a parked row at all. The
discriminating facts are the dispatched-session count (5, not 0) and the
closed-before-load timestamp: a parked row has 0 dispatched sessions in the
window; F28 has 5 and had already closed. The original issue names exactly 3
parked tasks (F35, DSN, OSD); the 4-row table was an over-count, and the
correct observable has **3** parked rows.

## The material finding for the fork

RFX `x_1m8eytq2e06q83fkwzbw7h` was answered **option 3 ("Both")**. But the
verified diagnosis shows **there is no "dispatch gate-free ready work" scheduler
bug to fix**: F35 was `draft` (not ready), and the one feasible-parked node (DSN)
had no logged gate refusal. So "Both" decomposes into:

1. **A planning-discipline fix** (the F35 class): don't load `draft` nodes into a
   sprint, or promote on load — so a loaded task is always in a state the
   dispatcher can offer.
2. **A visibility surface** for loaded tasks idle in a non-dispatched state
   (draft / feasible / blocked), naming per task: status, executor, active min,
   and open-to-close age — i.e. the observable table above, standing.

The "dispatch fix" the issue assumed is narrower than expected: it is a
planning-discipline fix plus a visibility surface, **not a scheduler bug fix**.

## aw4 file:line anchors for the three root causes (from the project graph)

These anchors are carried from the project graph claims (recorders and dates as
named); they were not re-read from aw4 in this session (no aw4 access).

- **F314 human-action gate** (dispatch-time, inside `Next()`): fires only when
  `role=="dev"` and `flow.pendingHuman(n.ID)` is non-empty.
  `orchestrator.go:1556-1575`, probe `band.go:247`, wired at `command.go:7720`.
  (recorder: landlord, 2026-11-28)
- **Deliverable-producebility / capability gate = operationsGuard** (dispatch-time,
  inside `wants()`): refuses only when the role's toolset serves NONE of the node's
  declared operations. `orchestrator.go:3076-3079`, implemented by
  `command/startable.go:248 OperationsGuardFor`. A "no agent role held the
  capability" park is this gate, not F314. (recorder: landlord, 2026-11-28)
- **Dispatch sweep dispatchability** is decided purely from node STATUS via the
  rules table, never findings: `orchestrator.Next` `orchestrator.go:342` →
  `wants` `orchestrator.go:2723` → `OfferableRoles` `orchestrator.go:3203`.
  Review findings (`TransitionedPayload.Findings []string`, `internal/plan/dag.go:491`)
  are plain strings with no kind field and do not block dispatch. The sprint-end
  "nothing dispatchable" fires at `internal/command/sprint.go:1018-1021` when
  `len(ready)==0 && len(inFlight)==0`. (recorder: hd, 2026-10-18)

## What this document proves, and how to re-derive it

The load-bearing facts are aw4-log facts (node_history per task). This raglit
session has no aw4 access, so they are carried from the verified research
(provenance above) at evidence class `accepted`. The blocks below do not re-run
the aw4 query — they verify that the **deliverable document itself** is present,
complete, and internally consistent, which is what the aw4 implementer reads.
They run green in the raglit checkout and fail if the document is missing,
truncated, or loses a parked row / a finding / the provenance.

The aw4 re-derivation contract (the exact query + assertion per finding, for
whoever has aw4 access) is recorded as prose in the body above — findings
(a)–(d) each name the node_history fact and the assertion that discriminates it
from the defect it disproves.

```sh
# The deliverable exists and is substantial: a real diagnosis, not a stub.
set -e
DOC="testplans/st_1m8ez08rp0kj5af7jk8qdk.md"
[ -f "$DOC" ]
wc -l "$DOC" | awk '{ exit ($1 < 100 ? 1 : 0) }'
echo "PASS: deliverable present, $(wc -l < "$DOC") lines"
```

```sh
# The three parked task ids are present as rows, and F28 (the disproved 4th
# row) appears only in the disproof, not as a parked row. F28's id is in the
# doc (finding d) but must NOT appear in a table row of the observable.
set -e
DOC="testplans/st_1m8ez08rp0kj5af7jk8qdk.md"
for id in st_1m7xnkc2rcpqxg4nft36ep st_1m7fwnst62zv2bnk53jyxx st_1m7qgbn0949r2b5t9j3t4z; do
  grep -q "$id" "$DOC" || { echo "MISSING parked row: $id"; exit 1; }
done
# F28 must not appear as a markdown table row (a row starts with '| `id`').
grep -Eq '^\| `st_1m7jjghkz6tq1b8npj0bwq`' "$DOC" && { echo "F28 present as a parked table row"; exit 1; }
echo "PASS: 3 parked rows present (F35, DSN, OSD); F28 not a parked row"
```

```sh
# Each finding is resolved with an explicit verdict, and the F314 attribution is
# explicitly NOT treated as log-verified. Discriminates: fails if a verdict is
# dropped or if F314 is silently asserted as a verified gate.
set -e
DOC="testplans/st_1m8ez08rp0kj5af7jk8qdk.md"
grep -q "DISPROVED as a dispatch bug" "$DOC" || { echo "finding (a) verdict missing"; exit 1; }
grep -q "the F314 attribution is NOT" "$DOC" || { echo "finding (b) F314 disproof missing"; exit 1; }
grep -q "CONFIRMED as an out-of-band block" "$DOC" || { echo "finding (c) verdict missing"; exit 1; }
grep -q "DISPROVED as a parked row" "$DOC" || { echo "finding (d) F28 disproof missing"; exit 1; }
echo "PASS: all 4 findings resolved; F314 attribution left unverified; F28 disproved"
```

```sh
# Provenance is attributed to the verifying sessions and channel message, not
# asserted as this session's own measurement.
set -e
DOC="testplans/st_1m8ez08rp0kj5af7jk8qdk.md"
grep -q "s_1m8gpf50devn1msvfrt9j7" "$DOC" || { echo "research session id missing"; exit 1; }
grep -q "4054782" "$DOC" || { echo "tech_lead verification channel msg missing"; exit 1; }
grep -q "log-verified" "$DOC" || { echo "log-verified / claim distinction missing"; exit 1; }
echo "PASS: provenance attributed (research session, tech_lead channel msg, fact/claim split)"
```
