import type { State, Stats, UnitStatus } from './types.js'

/**
 * THERE IS NO `resolve` IN THIS PACKAGE, AND THAT IS THE POINT.
 *
 * `Resolve(reading, log) → State` lives in Go and is the ONLY reader of the
 * vocabulary. Everything downstream reads `State`. That is a deliberate response
 * to a failure oidio hit: a renderer that kept its own idea of what `confirmed`
 * meant went on reporting the old coverage for days after a field was added,
 * understating how much had been reviewed. A second reader of this vocabulary
 * WILL go stale, so there is one — and putting one here, in the layer that ships
 * separately from the server, would be the worst possible place for the second.
 *
 * What this file holds is READING of an already-resolved State: derived questions
 * a UI asks that add no interpretation. Every one of them is a projection, and
 * none of them decides what a verdict means.
 */

/**
 * Whether every unit has been accounted for — read and accepted, or ruled on.
 *
 * `untouched` is the only state that means nothing is known. Confirmed and
 * affirmed both mean a reviewer went through the claim.
 */
export const complete = (s: Stats): boolean => s.total > 0 && s.untouched === 0

/**
 * How many units a person went to INDIVIDUALLY, as opposed to accepting under an
 * affirmation. Never added to `affirmed` — see the note on Kind.
 */
export const ruled = (s: Stats): number => s.confirmed + s.corrected + s.unclear + s.unsupported

/**
 * Whether a person actually retyped this unit's words.
 *
 * The only units that are ground truth for word error rate: scoring a recogniser
 * against text nobody disputed measures it against itself and reports a flawless
 * zero. `kind === 'corrected'` alone is not enough — a correction that changed
 * only the label left the text as the machine had it.
 */
export const textCorrected = (u: UnitStatus): boolean =>
  u.kind === 'corrected' && (u.text ?? '') !== (u.unit.text ?? '')

/** Untouched: nobody has ruled and no sweep covered it. */
export const untouched = (u: UnitStatus): boolean => !u.kind

/**
 * Provenance, as TWO axes reported separately.
 *
 * Were the claims accounted for, and were the words corrected. Conflating them is
 * how a partly-reviewed transcript gets cited as verified — a pass with a hundred
 * affirmations and no corrections is a complete review, and a pass with three
 * corrections and ninety untouched units is not, and one number cannot say which
 * you are holding.
 *
 * The affirmation's terms are QUOTED, never summarised. An affirmation is a
 * qualified claim with a materiality standard inside it, and paraphrasing it as
 * "the rest is right" changes what somebody attested to. An absent statement is
 * reported as absent rather than filled in.
 */
export interface Provenance {
  /** Every unit accounted for. */
  complete: boolean
  accounted: string
  corrections: string
  /** The reviewer's verbatim terms, or null when none were recorded. */
  terms: string | null
  /** True when a sweep happened but its terms were not recorded — an honest gap, said out loud. */
  termsMissing: boolean
}

export function provenance(s: Stats): Provenance {
  const done = complete(s)
  const swept = Boolean(s.swept_by || s.swept_at)
  const terms = s.swept_statement?.trim() ? s.swept_statement.trim() : null
  return {
    complete: done,
    // LOUD ONLY FOR `untouched`, because it is the only state that means nothing
    // is known. Everything else is a review that happened.
    accounted: done
      ? `${s.total} claim${s.total === 1 ? '' : 's'}, all accounted for — ${ruled(s)} ruled on individually, ${s.affirmed} affirmed`
      : `${s.untouched} of ${s.total} claim${s.total === 1 ? '' : 's'} NOT REVIEWED — nobody has looked at ${s.untouched === 1 ? 'it' : 'them'}`,
    corrections:
      s.corrected === 0
        ? 'no words were corrected'
        : `${s.corrected} unit${s.corrected === 1 ? '' : 's'} corrected`,
    terms,
    termsMissing: swept && terms === null,
  }
}

/**
 * The units a reviewer still has to look at, in reading order.
 *
 * Order is the server's — the effective list after any resegment spliced its
 * replacements in where the retired units were. Re-sorting here would move a
 * person's place in a document.
 */
export const outstanding = (s: State): UnitStatus[] => s.units.filter(untouched)

/**
 * Whether anything about this asset is in a state a reader must be warned about.
 *
 * Orphaned verdicts are the one that is easy to miss: they mean a re-read changed
 * what the machine claims and somebody's rulings no longer attach to anything.
 * They are never matched onto whatever is nearest, so they sit here until a
 * person accepts a rebinding.
 */
export const orphanCount = (s: State): number => s.orphaned?.length ?? 0
