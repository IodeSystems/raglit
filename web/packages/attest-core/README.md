# @iodesystems/attest-core

Machine reading, human verdict — the client half, with no framework in it.

An asset was read by a machine. The machine made claims about locatable pieces of
it. A person must be able to rule on those claims durably, with an honest account
of how much was actually ruled on. **Only the LOCATOR varies** — a span of time in
a recording, a box on a page, a run of bytes in a text, a binding between a
sentence and a claim.

The Go half is `raglit/attest`. Design and settled decisions: `raglit/plan/attest.md`.

## What is here

| | |
|---|---|
| `types.ts` | the wire shapes, mirroring the Go field for field |
| `transport.ts` | the six operations as an interface a host implements, plus an HTTP one for hosts that mount the standard routes |
| `state.ts` | projections of an already-resolved `State` |
| `workbench.ts` | where the reviewer is, what they have typed, and when it gets posted |

## What is deliberately NOT here

**`resolve`.** `Resolve(reading, log) → State` lives in Go and is the only reader
of the vocabulary; everything downstream reads `State`. That is a response to a
failure oidio hit — a renderer keeping its own idea of what `confirmed` meant went
on reporting the old coverage for days after a field was added. A second reader of
this vocabulary WILL go stale, so there is one, and a package that ships
separately from the server is the worst possible place for the second.

## Two injection points, and only two

- **A `Transport`**, because the host owns auth entirely. raglit mounts the
  service behind its own middleware, caselit behind `requireRole` and a per-case
  grant, the CLI as an authorized guest on loopback. A transport building its own
  requests would have to know which of those it was inside.
- **A unit renderer**, supplied by the React layer. It is the only thing that
  genuinely differs per medium: an audio turn with a waveform, a page crop, a
  sentence beside the claim it was bound to.

## The vocabulary

    confirmed   corrected   affirmed   unclear   unsupported   resegment   retract

`affirmed` is the **ordinary pass** and `confirmed` is the **escalation**. A low
confirmed count beside a high affirmed one is the shape of a careful pass, not a
thin one — a UI that sums them destroys the only signal saying which claims were
load-bearing enough to check twice.

`unsupported` is the one verdict that **subtracts**: the asset is silent, the
machine invented it, and a claim resting on nothing must stop counting as read.
`unclear` does not subtract — failing to read a scan is a fact about the scan, and
treating it as disproof would let a bad photocopy strip a document of its evidence.

## Test

    npm test
