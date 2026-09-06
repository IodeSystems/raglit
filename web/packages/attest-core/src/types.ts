/**
 * The wire shapes, mirroring `raglit/attest`.
 *
 * THESE ARE TYPES, NOT A SECOND MODEL. Every field here exists in the Go and is
 * named the same way, because a translation layer between them is exactly where
 * x and y swap and where a verdict quietly changes meaning.
 */

/**
 * The vocabulary, converged.
 *
 * Four repos built human attestation separately — oidio over audio turns, raglit
 * over page regions, kgraph over (fact, source) edges — and only the LOCATOR ever
 * differed. kgraph's `attested` and `illegible` were the same two concepts as
 * `confirmed` and `unclear`, spelled differently; they are read and normalised
 * server-side and never appear here.
 *
 * `affirmed` is THE ORDINARY PASS and `confirmed` is the escalation. A reviewer
 * goes through the asset, edits what needs it, and moves past what they agree
 * with; the affirmation covers everything they passed over, so an affirmed unit
 * WAS read. `confirmed` is for the claim something depends on. A low confirmed
 * count beside a high affirmed one is the shape of a careful pass, not a thin
 * one, and a UI that sums them destroys the only signal saying which claims were
 * load-bearing enough to check twice.
 */
export type Kind =
  | 'confirmed'
  | 'corrected'
  | 'affirmed'
  | 'unclear'
  | 'unsupported'
  | 'resegment'
  | 'retract'

/**
 * Which verdicts a person applies to ONE unit from the workbench.
 *
 * `resegment` is absent because it replaces units rather than ruling on one, and
 * `retract` because it is an undo rather than a verdict — both are real entries
 * and neither belongs on a row of verdict buttons.
 */
export const UNIT_VERDICTS = ['confirmed', 'affirmed', 'corrected', 'unclear', 'unsupported'] as const
export type UnitVerdict = (typeof UNIT_VERDICTS)[number]

/**
 * What each verdict MEANS, in the reviewer's terms rather than the format's.
 *
 * Carried here so every host says the same thing. Two of these are the ones
 * people get wrong, and the gloss is where that gets prevented rather than in a
 * doc nobody opens.
 */
export const VERDICT_GLOSS: Record<UnitVerdict, string> = {
  confirmed: 'Checked deliberately, because something depends on this one.',
  affirmed: 'Read in the ordinary pass, and nothing here needed changing.',
  corrected: 'The machine was wrong. What it should say goes in the correction.',
  unclear:
    'Looked, cannot tell — a verdict on the ARTIFACT, not the claim. It does not subtract: failing to read a scan is a fact about the scan.',
  unsupported:
    'The asset is SILENT here — the machine invented it. This is the one verdict that subtracts, because a claim resting on nothing must stop counting as read.',
}

/** An asset kind. Selects which locator is meaningful and what a reviewer must be shown. */
export type AssetKind = 'audio' | 'image' | 'pdf' | 'text'

export interface Asset {
  id: string
  name?: string
  kind: AssetKind
  sha256?: string
}

/** Seconds from the start of a recording. */
export interface TimeSpan {
  start: number
  end: number
}

/** A box in normalized 0..1 page coordinates, so it survives a re-render at another dpi. */
export interface Rect {
  x: number
  y: number
  w: number
  h: number
}

export interface Area {
  page: number
  rect: Rect
  rotation?: number
  dpi?: number
}

/** A range of an asset's canonical text, in BYTES from its start — never an index into a fragment. */
export interface Span {
  from: number
  to: number
}

/** Exactly one field is set. */
export interface Locator {
  time?: TimeSpan
  area?: Area
  span?: Span
}

/**
 * One machine claim about one piece of the asset. IMMUTABLE — every human change
 * is a log entry, never an edit to this.
 */
export interface Unit {
  id: string
  parent?: string
  locator: Locator
  /** What the machine read here. */
  text?: string
  /**
   * The machine's CATEGORICAL claim: the diarizer's cluster, the region's kind,
   * the claim id a sentence was bound to. Kept apart from `text` because the two
   * fail differently — the words can be right while the speaker is wrong.
   */
  label?: string
}

/** One line of the append-only log. */
export interface Entry {
  kind: Kind
  unit?: string
  /** Applies to every unit with no ruling EARLIER IN THE LOG. Only with `affirmed`. */
  blanket?: boolean
  text?: string
  label?: string
  note?: string
  /** The reviewer's own words on a blanket affirmation. Never generated. */
  statement?: string
  units?: Unit[]
  supersedes?: string[]
  /** The PERSON who ruled. Self-declared and required. */
  by: string
  /** The account they were authorized under. Never settable by a caller. */
  auth?: string
  at?: string
}

/** One unit's effective state: what the machine claimed, and what a person did about it. */
export interface UnitStatus {
  unit: Unit
  /** The ruling in force, or absent for untouched. */
  kind?: Kind
  /** What this unit says NOW — the correction where one was made, the machine's reading otherwise. */
  text?: string
  label?: string
  note?: string
  by?: string
  auth?: string
  at?: string
  /** A unit a person created with a resegment, rather than one the machine proposed. */
  authored?: boolean
  /**
   * A ruling that came from a blanket affirmation rather than a line about this
   * unit. NOT a demotion — the reviewer went through it, and the affirmation they
   * signed says under what terms.
   */
  swept?: boolean
}

/**
 * The completeness account. The states are reported SEPARATELY and never
 * collapsed into one percentage, because a single number hides which failure
 * produced it.
 */
export interface Stats {
  total: number
  confirmed: number
  corrected: number
  affirmed: number
  unclear: number
  unsupported: number
  /** The one that means NOTHING IS KNOWN. */
  untouched: number
  authored: number
  swept_by?: string
  swept_at?: string
  /** The reviewer's own words. Empty means the terms were not recorded, not that there were none. */
  swept_statement?: string
}

/** A reading and its verdicts, resolved. Computed server-side — see `resolve` in state.ts. */
export interface State {
  asset: Asset
  producer?: string
  units: UnitStatus[]
  stats: Stats
  /**
   * Verdicts that rule on units this reading no longer contains — the cost of a
   * re-read that changed what the machine claims. Reported rather than dropped
   * and never matched onto whatever is nearest: a verdict silently re-attached to
   * a claim nobody ruled on is a FALSE attestation, which is worse than a lost one.
   */
  orphaned?: Entry[]
}

/**
 * One row of the asset list, and it is FLAT — mirroring the Go field for field.
 *
 * `asset` is the root-relative path and the handle every other operation takes,
 * not a nested Asset. This was written as `{asset: Asset}` first, which
 * typechecked perfectly and would have rendered "[object Object]" against a live
 * daemon: the types in this file are a mirror, and inventing a shape here is
 * exactly the translation-layer hazard the file header warns about.
 */
export interface AssetRef {
  asset: string
  kind?: string
  producer?: string
  stats: Stats
}
