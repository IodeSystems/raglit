import assert from 'node:assert/strict'
import { test } from 'node:test'
import { Workbench } from './workbench.js'
import { provenance, ruled, textCorrected } from './state.js'
import type { Transport, VerdictInput } from './transport.js'
import type { State, Stats, UnitStatus } from './types.js'

function unit(id: string, over: Partial<UnitStatus> = {}): UnitStatus {
  return { unit: { id, locator: { span: { from: 0, to: 1 } }, text: `machine ${id}` }, ...over }
}

function stats(over: Partial<Stats> = {}): Stats {
  return {
    total: 0,
    confirmed: 0,
    corrected: 0,
    affirmed: 0,
    unclear: 0,
    unsupported: 0,
    untouched: 0,
    authored: 0,
    ...over,
  }
}

function fakeTransport(units: UnitStatus[]) {
  const posted: VerdictInput[] = []
  let fail = false
  const t: Transport = {
    assets: async () => [],
    state: async (): Promise<State> => ({
      asset: { id: 'a', kind: 'text' },
      units,
      stats: stats({ total: units.length, untouched: units.length }),
    }),
    verdict: async (input) => {
      if (fail) throw new Error('the daemon is down')
      posted.push(input)
      return {}
    },
    sweep: async () => ({}),
    resegment: async () => ({}),
    evidenceURL: () => '',
  }
  return { t, posted, setFail: (v: boolean) => (fail = v) }
}

// A CORRECTION IS NOT POSTED PER KEYSTROKE.
//
// The log is append-only — that is what lets two syncing machines merge without
// losing a verdict — and a line per keystroke would bury one ruling under two
// hundred, destroying the record the format exists to keep.
test('typing does not post; the idle timer does, once', async () => {
  const { t, posted } = fakeTransport([unit('u1')])
  let fire: null | (() => void) = null
  const capture = (fn: () => void) => {
    fire = fn
  }
  const w = new Workbench({
    transport: t,
    assetId: 'a',
    by: 'carl',
    setTimeoutImpl: ((fn: () => void) => {
      capture(fn)
      return 1 as unknown as ReturnType<typeof setTimeout>
    }) as unknown as typeof setTimeout,
    clearTimeoutImpl: (() => {}) as unknown as typeof clearTimeout,
  })
  await w.load()

  w.edit({ text: 'a' })
  w.edit({ text: 'ab' })
  w.edit({ text: 'abc' })
  assert.equal(posted.length, 0, 'three keystrokes must not be three log lines')
  assert.equal(w.snapshot().pending.get('u1')?.text, 'abc', 'the edit is held so the UI can show it')

  const scheduled = fire as null | (() => void)
  assert.ok(scheduled, 'an edit must schedule a post')
  scheduled!()
  await new Promise((r) => setImmediate(r))
  assert.equal(posted.length, 1, 'one post for the whole burst')
  assert.equal(posted[0]?.text, 'abc')
  assert.equal(posted[0]?.kind, 'corrected')
  assert.equal(posted[0]?.by, 'carl', 'the person is on every line and is never defaulted')
})

// A DROPPED CORRECTION IS A REVIEWER'S WORK SILENTLY DISCARDED.
test('a failed post puts the edit back rather than losing it', async () => {
  const { t, setFail } = fakeTransport([unit('u1')])
  const w = new Workbench({ transport: t, assetId: 'a', by: 'carl', idleMs: 0 })
  await w.load()
  w.edit({ text: 'the corrected words' })
  setFail(true)
  await w.flush()
  assert.equal(
    w.snapshot().pending.get('u1')?.text,
    'the corrected words',
    'a correction that failed to post must survive in pending',
  )
  assert.match(w.snapshot().error ?? '', /daemon is down/)
})

// Moving away from a unit is the same signal as blurring it.
test('moving flushes the pending edit', async () => {
  const { t, posted } = fakeTransport([unit('u1'), unit('u2')])
  const w = new Workbench({ transport: t, assetId: 'a', by: 'carl', idleMs: 0 })
  await w.load()
  w.edit({ text: 'half-typed' })
  w.move(1)
  await new Promise((r) => setImmediate(r))
  assert.equal(posted.length, 1, 'an edit left behind is a correction the reviewer believes they made')
})

// A re-read that dropped units must not throw a reviewer back to the top of a
// document they were halfway through.
test('the cursor is clamped on reload, not reset', async () => {
  const three = [unit('u1'), unit('u2'), unit('u3')]
  const { t } = fakeTransport(three)
  const w = new Workbench({ transport: t, assetId: 'a', by: 'carl', idleMs: 0 })
  await w.load()
  w.goto(2)
  assert.equal(w.current?.unit.id, 'u3')
  three.pop()
  await w.load()
  assert.equal(w.current?.unit.id, 'u2', 'landing on the last unit that still exists is the closest true position')
})

test('a snapshot is a new object, or nothing repaints', async () => {
  const { t } = fakeTransport([unit('u1')])
  const w = new Workbench({ transport: t, assetId: 'a', by: 'carl', idleMs: 0 })
  await w.load()
  const before = w.snapshot()
  w.edit({ text: 'x' })
  assert.notEqual(w.snapshot(), before, 'a snapshot mutated in place is invisible to a framework')
  assert.notEqual(w.snapshot().pending, before.pending, 'the Map must be new too')
})

// `affirmed` is the ORDINARY pass and `confirmed` is the escalation. Summing them
// destroys the only signal saying which claims were load-bearing.
test('ruled counts individual rulings and never folds in the sweep', () => {
  const s = stats({ total: 100, confirmed: 3, corrected: 2, unclear: 1, unsupported: 1, affirmed: 93 })
  assert.equal(ruled(s), 7)
})

// Conflating the two axes is how a partly-reviewed transcript gets cited as
// verified.
test('provenance is loud only for untouched, and quotes the terms', () => {
  const done = provenance(
    stats({
      total: 10,
      confirmed: 2,
      affirmed: 8,
      swept_by: 'carl',
      swept_statement: 'reasonably certain there are only minor errors',
    }),
  )
  assert.equal(done.complete, true)
  assert.equal(done.terms, 'reasonably certain there are only minor errors')
  assert.equal(done.termsMissing, false)
  assert.match(done.accounted, /all accounted for/)

  const partial = provenance(stats({ total: 10, confirmed: 1, untouched: 9 }))
  assert.equal(partial.complete, false)
  assert.match(partial.accounted, /NOT REVIEWED/, 'untouched is the only state that means nothing is known')

  // A sweep with no recorded terms says so rather than inventing any.
  const bare = provenance(stats({ total: 2, affirmed: 2, swept_by: 'carl' }))
  assert.equal(bare.terms, null)
  assert.equal(bare.termsMissing, true)
})

// Scoring a recogniser against text nobody retyped measures it against itself.
test('a correction that changed only the label is not a text correction', () => {
  const u = unit('u1', { kind: 'corrected', text: 'machine u1', label: 'someone else' })
  assert.equal(textCorrected(u), false)
  const v = unit('u2', { kind: 'corrected', text: 'what it actually says' })
  assert.equal(textCorrected(v), true)
})
