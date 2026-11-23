# R1 Findings: Sprint 79 dispatch_wait Reconciliation

## Headline reconciliation

- Issue cites 192.5 min. The ledger `spans` array carries `dispatch_wait` = **194.3 min**
  (window.asOf 12:07:03 PDT, end 19:06:13Z). The 192.5 is the same span read at an
  earlier as-of; ~2 min as-of/rounding difference. Not two different spans.

- `dispatchWaitQueueMin` (122.4) + `dispatchWaitSiblingMin` (513.8) = 636.2 min.
  These are **separate derived aggregations, not a partition of the 194.3 span**.
  Their "shares" (0.63 + 2.64 = 3.27) are ratios, not fractions summing to 1.

## The dispatchWaitBySession map is mislabeled

Every value in `dispatchWaitBySession` equals that session's total wall lifetime
(session.started → session.ended), not a ready→start gap. The 23 lifetimes overlap
(5 concurrent nodes) and sum to 1138.2 min of session-time.

| session | node | started | ended | wait(map) | lifetime | match |
|---|---|---|---|---|---|---|
| s_1m4qjew6qcv5xctygybkax | st_1m4qbepwp0cn132yf0m1ba | 01:07:56 | 01:33:07 | 25.2 | 25.2 | Y |
| s_1m4qmyybw7rmgzame0cxfv | st_1m4qbepwp0cn132yf0m1ba | 01:51:40 | 03:19:40 | 88.0 | 88.0 | Y |
| s_1m4qv633vbnhj9k2g6zret | st_1m4qbergnbx5r0dmn25q06 | 03:40:25 | 06:22:04 | 161.6 | 161.6 | Y |
| s_1m4r28nrd4ebheeg38eb6q | st_1m4qbeqq78deeqhn872pv3 | 05:44:10 | 07:44:04 | 118.8 | 118.8 | Y |
| s_1m4r2b6pe7qzg687pwvpsd | st_1m4qbeq9c3dcb3xzv70gz6 | 05:45:33 | 08:10:19 | 144.8 | 144.8 | Y |
| s_1m4rc1j854m2k413tqy3am | st_1m4qbeq9c3dcb3xzv70gz6 | 08:35:03 | 10:11:08 | 96.1 | 96.1 | Y |
| s_1m4rdq1js434e821nc5b25 | st_1m4qber3wbrssc3zat4ybw | 09:04:15 | 10:06:04 | 61.8 | 61.8 | Y |

(7 of 23 shown; all 23 match: map value == lifetime)

## Real ready→start gap

session.started → first turn.started = ~3 seconds (0.05 min) for all 23 sessions.
One outlier at ~1 min (s_1m4r9c0zmaq6tbbyg4dcsh). No per-session queue or slot
wait of the magnitude the map suggests.

## No slot events in sprint 79

`slot.waited` / `slot.waiting` / `session.pool_waiting` event kinds all begin
2026-10-14, two days after this sprint (window ends 19:06:13Z 10-12). The
hypothesized node-level sibling slot contention has no event evidence here.

## Where the 194.3 min sits: node-level feasible→dev-start gaps

The `dispatch_wait` span is a derived residual: wall time where work was
ready/owed but no session was running. It concentrates in the gaps between a
node becoming dispatchable (plan.transitioned to=feasible) and the next dev
session starting (plan.transitioned to=active, role=dev):

| node | feasible (plan.transitioned) | dev active (plan.transitioned) | gap |
|---|---|---|---|
| st_1m4qbepwp0cn132yf0m1ba | 01:31:24 | 01:52:06 | 20.7 min |
| st_1m4qbergnbx5r0dmn25q06 | 03:26:29 | 03:41:12 | 14.7 min |
| st_1m4qbeq9c3dcb3xzv70gz6 | 05:21:17 | 05:46:48 | 25.5 min |
| st_1m4qbeqq78deeqhn872pv3 | 05:02:33 | 05:45:21 | 42.8 min |
| st_1m4qber3wbrssc3zat4ybw | 08:33:10 | 09:04:52 | 31.7 min |

Sum of these 5 gaps: 135.4 min. The residual also includes smaller inter-node
and inter-sprint gaps that bring the total to 194.3 min.

## Mechanism (reframed)

dispatch_wait is a **derived residual** — wall time where work is ready/owed but
no session is running — NOT event-tracked ready→start gaps and NOT slot
contention. The 5.5x regression is the **dispatch-loop cadence**: the delay
between a node becoming dispatchable (feasible) and the next sweepDispatch pass
starting a dev session for it. The "single task × slot/grant machinery"
hypothesis is killed; the real driver is sweep-pass cadence over the 5-node
ready set.

## Caveat

The exact sweep cadence / why it widened vs sprint 78 (34.8 min) requires
reading aw4 source (internal/cli/dispatcher.go sweepDispatch) — a separate
checkout. That is the follow-up for R2.
