# Research: Sibling-Slot Dispatch Wait — Mechanism and Minimal Fix

## 1. The Slot Mechanism That Serializes Siblings

The serialization is NOT an explicit gate in aw4's dispatch code. There is no
"one slot per stream" rule in the orchestrator. What exists:

1. **Per-project SCM slot, capacity 1.** Each project has a `project_properties`
   row with class `scm` and a `capacity` field. For raglit (like bintag,
   fleet-ops, hd) the capacity is 1 with no `manual` key, so the automated
   pool is 1 and the manual pool is 0.
   - Source: aw4 researcher claim (2026-10-27): "Projects with no manual key
     (bintag, raglit, fleet-ops, hd, etc.) read automated=capacity=1."

2. **Dispatch loop acquires the scm slot per subtask.** The dispatch sweep
   (`dispatchPass` in `internal/cli/orchestrate.go`, line 2727+) iterates
   ready subtasks. For each dev subtask it calls `mgr.TryAcquireFor`
   (`internal/cli/orchestrate.go:3442`, delegating to `internal/slots/slots.go:150/181`)
   to lease one scm index. With capacity 1,
   only one subtask can hold the slot at a time.
   - Source: fleet-ops researcher claim (2026-10-25): "slots.Manager.TryAcquireFor
     (slots/slots.go:150/181) hands out the single free index."

3. **Second subtask is skipped, not blocked.** When `TryAcquireFor` fails
   (no free index), the dispatch pass records `rec.hold(HoldCapacityHeld)` at
   orchestrate.go:3448-3452 and moves on. The subtask stays in the ready queue and
   is re-offered on the next sweep pass.
   - Source: fleet-ops researcher claim (2026-10-26): "The pass does NOT change
     the plan node's status on a capacity refusal — it only writes the
     dispatch_hold aggregate."

4. **Sprint membership is not a gate.** The dispatch loop offers slots to any
   in-flight ready subtask in the project regardless of sprint membership.
   Contention is purely from the single scm slot.
   - Source: fleet-ops researcher claim (2026-11-23): "The dispatch loop in aw4
     offers slots to any in-flight ready subtask in the project regardless of
     sprint membership."

**Conclusion:** The mechanism is the per-project scm capacity of 1. Two
independent sibling subtasks in the same raglit stream each need one scm slot.
With one slot, the second waits for the first to release it. This is the
measured cause of the 54.3-min dispatch_gap in sprint 237 (85% of wall time).

## 2. Candidate Fix A: Raise SCM Capacity for raglit

**Change:** Set raglit's scm capacity to 2 (or higher).

**Command:**
    aw4 project property raglit --class scm --capacity 2 --by <actor>

**Gate:** The write is gated by `gateIsolation`. It is refused unless
raglit's isolation spec (the `scm` config `isolation_spec`, else the live D4
"suite at once" spec) reads `kept` in the project's latest conformance.
A missing spec is seeded and the write refused.
- Source: dun researcher claim (2026-11-23): "A raise above 1 is GATED by
  gateIsolation: it is refused unless the project's isolation spec ... reads
  `kept` in the project's latest conformance."

**Prerequisite:** raglit must have an isolation spec that is `kept` in its
conformance. The spec would need to demonstrate that two raglit worktrees can
run the suite concurrently and both pass. This is the same D4 pattern used by
other projects (gwag, bintag).

**Effect:** With capacity 2, two dev subtasks can hold scm slots simultaneously.
The dispatch pass hands the second index to the second subtask on the same
pass, eliminating the wait.

**Other knobs that must also be sufficient:**
- `sessionPool.max` / `--sessions` flag (default 2): must be >= 2 to actually
  run two sessions. Source: fleet-ops researcher claim (2026-11-01): "The
  dispatch cap is the daemon --sessions flag (default 2)."
- `model_gate_capacity` (global, default 1): bounds model calls, not scm slots.
  Two sessions can run concurrently but only one model call at a time. This
  reduces the gain if sessions are model-bound. Source: bintag researcher
  claim (2026-10-17): "model_gate_capacity is a single global table row."
- `review_slot_reserve`: must not consume the second scm slot.

**Minimal change for Fix A:**
1. Write and keep raglit's isolation spec (D4: two worktrees run the suite at
   once and both pass). This is a raglit-side task.
2. Run the CLI command to set capacity to 2.
3. No code change in aw4.

**Risk:** If raglit's test suite is not safe for concurrent runs (shared state,
port conflicts, temp file collisions), the isolation spec cannot be kept and
the gate refuses the write.

## 3. Candidate Fix B: Concurrent Dispatch of Independent Siblings (aw4 Code Change)

**Change:** Modify the dispatch sweep to allow independent sibling subtasks
(no requires-edge between them) to dispatch concurrently, even within a
single scm slot allocation.

**Why this is harder:** The scm slot IS the worktree lease. Two subtasks need
two worktrees. The slot mechanism exists precisely to limit concurrent
worktrees. Removing the per-subtask slot check for siblings would mean:
- Two subtasks sharing one worktree (unsafe: concurrent git operations).
- Or allocating a worktree without a slot (bypassing the capacity model).

The fleet-ops researcher (2026-10-25) explicitly states: "Parallelizing two
streams requires raising scm capacity (plus review_slot_reserve,
sessionPool.max/--sessions, model lanes, and F223 sprint slot grants)."

There is no code path in the dispatch sweep that grants slots conditionally
based on sibling independence. The `TryAcquireFor` call is uniform: it asks
for one scm index and gets one or nothing. Adding sibling-awareness would
require:
- Detecting the sibling relationship in `dispatchPass` (the ready queue rows
  carry plan/stream membership).
- A new slot class or a slot-granting path that allows N worktrees for
  siblings of the same plan.
- Changes to `slots.Manager` to support per-plan slot pools.

This is a significant architectural change to the slot model, not a minimal
fix.

## 4. Recommendation

**Fix A (raise scm capacity) is the minimal change.** It is a configuration
write, not a code change. The prerequisite — raglit's isolation spec being
`kept` — is the real work item.

**Change points for Fix A:**
1. **raglit (this project):** Author and keep the D4 isolation spec.
   Two raglit worktrees run `go test ./...` concurrently and both pass.
   This is the blocking prerequisite.
2. **aw4 (operator action):** Run the CLI command to set capacity to 2 after
   the spec is kept.
3. **aw4 (operator action, if needed):** Verify `--sessions` >= 2 on the daemon
   hosting raglit (default is 2, so likely already sufficient).

**What spec the fix satisfies:** F70 (The sweep's dispatch policy) is the
correct feature. No existing spec covers sibling slot concurrency. A new spec
under F70 would state: "Independent sibling subtasks in a loaded stream
dispatch concurrently when scm capacity >= 2." An RFX to the aw4 PM is needed
to approve this spec.

## 5. What Cannot Be Verified From This Checkout

The aw4 source is not in this checkout. All mechanism claims are from
graph-asserted research by fleet-ops, dun, aw4, hd, bintag, and landlord
researchers who read aw4's source directly. Line numbers and function names
are as recorded by those researchers; they were not re-verified against the
aw4 tree in this session.

The sprint 237 KPI figures (54.3 min gap, 85% dispatch_gap, 0% active) are
from the issue text, not re-measured here.
