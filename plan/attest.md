# attest — machine reading, human verdict

> How this plan works: see `~/CLAUDE.md` § Planning. Status marks: ◻ todo ·
> ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

An asset was read by a machine. The machine made claims about locatable pieces
of it. A person must be able to rule on those claims durably, with an honest
account of how much was actually ruled on.

**Only the LOCATOR varies.** A span of time in a recording, a box on a page at a
rotation and a dpi, a run of characters in a text, a (fact, source) edge.

- `<asset>.reading.json` — what the machine claimed. Flat `[]Unit` with `Parent`
  references, **not** nested: a digest-addressed parent must not change id when a
  descent finds something new underneath it.
- `<asset>.attest.jsonl` — append-only, what people decided, last relevant entry
  wins.
- Unit ids are content-addressed, so a verdict survives a re-read or is reported
  as ORPHANED. It is never silently re-attached to a claim nobody ruled on.

## Where it lives — settled 2026-08-20

**`raglit/attest` is canonical.** Carl's call.

It was extracted into a standalone repo, `github.com/iodesystems/attest`, in
July, on the intended pattern of a sibling checkout consumed by
`replace ../attest`. raglit then **vendored it instead of importing it**, and the
copy drifted: `Status` became `UnitStatus`, `service.go` grew by 69 lines, and
three UI fixes landed here that the original never got — rendering against the
addressed asset, text evidence no longer rendered as an audio player, and a
workbench usable on a phone. The standalone's last commit is 2026-07-31; this
copy moved on 2026-08-01 and has moved since.

So the deduplication had itself been duplicated. Rather than reconcile two
versions of the thing that exists to stop there being two versions, the fork —
which is ahead, on `main`, and the only one anything actually runs — becomes the
original. The standalone repo carries a retirement notice and is not deleted.

**Nothing was lost in the retirement.** `Span` and `KindText`, the standalone's
last two commits, are both here. The only files that existed solely there were
`cmd/attest`, superseded by raglit's own CLI integration (`attesttext.go`,
`attestaudio.go`), `cmd/demo`, and this plan.

**What this costs, and it is the reason it was a repo:** a consumer now depends
on RAGLIT to attest anything. For kgraph and caselit that is free — both already
depend on raglit. For **oidio it is a new and inverted dependency**, because
oidio depends on nothing today. See the producer table below.

## Settled decisions

Carried from the standalone's plan. They still hold.

- **Sidecar-first; the service indexes, it does not own.** Verdicts live in
  append-only `<asset>.attest.jsonl` beside the asset. The service reads and
  writes those files and adds no authority of its own — it can be deleted without
  data loss, and a producer keeps working offline.
- **Append-only because these files sync between machines.** Two machines
  rewriting one JSON object is a merge conflict and a lost verdict; two machines
  appending lines is a merge a person can read. It is also the only structure in
  which "who said what, when" survives.
- **Content-addressed unit ids.** Producers renumber — a re-read proposes
  different regions, a join/split rewrites the list. With ordinal ids a verdict
  on unit 14 becomes a verdict about whatever unit 14 is now: not a lost verdict,
  a **false** one.
- **Library + huma, not just a binary.** `Service.Register(api, prefix)` so a
  host mounts the review API into its own router with its own auth.
- **The host owns auth entirely, and TWO names go on every verdict.** attest
  authenticates nobody. `Auth` is the resolved principal, never settable by a
  caller; `By` is the self-declared person, required, from the request. An
  attorney hands a paralegal the link and the paralegal does the review —
  recording only the principal files the paralegal's work under the attorney's
  name, and recording only the author lets any link-holder type anything.
  Together they say what happened: authorized as X, performed by Y.
- **The unit is immutable; every human change is a log entry.** oidio's original
  bug was one `speaker` field holding both the diarizer's grouping and the
  human's correction, which made a local fix and a global identity claim
  indistinguishable.
- **No confidence scores.** Verdicts are categorical. "The document does not say
  this" is not 0.3 of anything.
- **`affirmed` is the ORDINARY pass; `confirmed` is the escalation.** Corrected
  2026-07-29 after the first description got it backwards. A reviewer goes
  through the asset, edits what needs it, and moves past what they agree with;
  the affirmation covers everything they passed over, so an affirmed claim WAS
  heard. `confirmed` is for the claim something depends on. A low confirmed count
  beside a high affirmed count is the normal shape of a careful pass, not a thin
  review.
- **The affirmation's terms are the reviewer's words, quoted verbatim.** An
  affirmation is a qualified claim with a materiality standard inside it, and the
  tool paraphrasing it as "the rest is right" changes what was attested to.
- **`Resolve(reading, log) → State` is the ONLY reader of the vocabulary.**
  Everything downstream reads `State`. oidio proved why: a second reader with its
  own idea of `confirmed` reported stale coverage for days.
- **Blanket affirmation is positional** — one log line covering units untouched
  *earlier in the log*. Never overwrites an individual ruling; never reaches
  forward over units added later.

## The vocabulary — the convergence target

    confirmed   corrected   affirmed   unclear   unsupported   resegment   retract

Merged from oidio's Confirmed/Affirmed/Unclear/Corrected and kgraph's
attested/corrected/unsupported/illegible. `unsupported` is the one verdict that
SUBTRACTS: the asset is silent, the machine invented it, and a claim resting on
nothing must stop counting as read. `unclear` does not subtract — failing to read
a scan is a fact about the scan, and treating it as disproof would let a bad
photocopy strip a document of its evidence.

## Producers

| producer | locator | state |
|---|---|---|
| **raglit** | `Area{page,bbox,rotation,dpi}` | ✅ shipped. Carries raglit's own per-region SHA256 through as evidence, so crops verify with no new machinery. The join back to a region is by recorded GEOMETRY, never by id, because attest content-addresses its units. |
| **raglit** | `Span` — text with no geometry | ✅ shipped, `cmd/raglit/attesttext.go` |
| **raglit** | `Time{start,end}` — audio | ✅ shipped, `cmd/raglit/attestaudio.go` |
| **oidio** | `Time{start,end}` | ❌ **NOT SHIPPED.** The standalone's plan recorded this as done; oidio's history contains no attest code on any branch. Its `internal/verify` (514 lines, Go serving hand-written HTML) is still its own separate implementation. |
| **kgraph** | (fact, source) edge | ❌ separate implementation, `attest.go`, 378 lines, own vocabulary |
| **caselit** | (sentence, claim) binding | ◻ next — see below |

## Active work

### ◻ Converge kgraph's vocabulary — Carl's call, 2026-08-20

kgraph's `attest.go` is a fourth implementation with its own words:
`attested` → `confirmed`, `illegible` → `unclear`, and no `affirmed` at all, so
it cannot express the ordinary pass. The semantics already agree everywhere else:
kgraph's `unsupported` is "the document is SILENT on this. Not refuted: absent",
which is this package's `unsupported` exactly.

**next** — rename the two, add `affirmed`, then decide whether kgraph keeps its
own 378 lines or its (fact, source) unit becomes a `Locator` variant here. The
second is the real prize and the standalone plan already listed it as the
unification of the last of the three.

**risks** — existing `.jsonl` lines carry the old words. A reader must accept
both and normalise, or a migration must rewrite logs that are append-only by
design and therefore should not be rewritten. Accepting both on read is the
honest option.

### ◻ caselit's locator: a binding — Carl's call, 2026-08-20

The unit is (sentence, claim) and the question is "does this claim say what this
sentence says". Every verdict maps without strain, and `unsupported` is the
valuable one: it means the binding is a STRETCH — the claim is about the same
subject and does not assert the sentence — and it subtracts, returning the
sentence to unsupported where it belongs.

**Why it earns its place immediately:** a binding is written DIRECTLY, with no
approval gate, because the fact tree must not grow at the speed of a human
reading. A recent pass on a live matter wrote 164 bindings in one run, and "a
plausible-but-wrong binding" is the top risk on that runbook precisely because
nothing gates the write. This is that gate, applied after the fact, which is the
right shape for it — the same shape as every other verdict here.

**next** — a `Binding{sentence, claim}` locator variant, and an `Evidence`
rendering that shows the sentence beside the claim body rather than a crop.

### ◻ The TS libs and headless UI — Carl's call, 2026-08-20

`ui.html` is 1,060 lines of vanilla HTML embedded by `//go:embed`. It is good,
and it has already been re-implemented once: raglit's own SPA carries
`web/src/panes/Attest.tsx` and `AttestAsset.tsx`, 405 lines of React doing the
same workbench. That is a third rendering of one screen, inside this repo.

**The stacks already agree, which is what makes the extraction cheap** — raglit
`web/` and caselit `ui/` are both React 19, MUI ^9, @emotion ^11, TanStack
Router. This is an extraction, not a convergence project.

**Shape:**

- **core** — framework-agnostic TS, no DOM. Verdict algebra, append-only log
  semantics, content-addressed ids, orphan reconciliation, the keyboard flow,
  and `Resolve` mirrored so a client can render state without a round trip.
  Testable in node. THE VOCABULARY IS SETTLED ONCE, HERE.
- **react** — hooks over the core plus a reference MUI workbench, with exactly
  two injection points: a **transport adapter**, so a host keeps its own HTTP
  surface and its own auth, and a **unit renderer**, which is the only thing that
  genuinely differs — an audio turn with a waveform, a page crop, a sentence
  beside a claim.

**risks** — oidio has no build step and no TS at all, so adopting the UI means
adding vite and embedding a bundle. raglit's daemon already does exactly that, so
there is a pattern to copy rather than invent. And a fourth rendering of the
workbench is the failure mode: the React panes must become the reference UI
rather than sit beside it.

**blocking decisions** — none outstanding. Vocabulary convergence and the binding
locator were both decided 2026-08-20.

## Risks / untested

- Content addressing orphans a corpus when the recogniser changes. Correct
  (nobody ruled on the new claim) and occasionally expensive. The mitigation is a
  **rebinding pass** proposing matches for a person to accept. Never silent.
- `Resegment` adds units as *untouched*. Arguably a person who retypes a turn has
  corrected it; keeping it uniform is simpler and the UI can emit both entries.
- The author name is self-declared and proves nothing by itself — that is what a
  signature on a review sheet has always been. The guarantee is the other half:
  the principal cannot be forged, so "under whose authority" always has a true
  answer.
- `Assets` resolves every asset on every listing call. Fine for a hearing's worth
  of files, untested against a corpus in the thousands.
- A correction posts on a 1.5s idle, on blur and on unload — NOT per keystroke.
  The log is append-only and a line per keystroke would bury one ruling under two
  hundred. The trade is a ~1.5s window where a hard kill loses the last edit.
- ⏸ **Audio playback is unverified in a browser.** The automation Chrome never
  reaches `loadedmetadata` over plain HTTP; the likely cause is that it will not
  decode media on an insecure origin, and a `blob:http://…` URL inherits the same
  insecure origin so the earlier blob test ruled nothing out. TLS works
  (`--tls-cert/--tls-key`, curl gets 200). **Resume condition:** the browser host
  does not trust this box's mkcert CA. Installing a root CA on another machine
  and clicking through a cert interstitial are both security decisions, so
  neither was taken.
