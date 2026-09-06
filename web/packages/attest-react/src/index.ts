/**
 * @iodesystems/attest-react — headless React bindings for the attest workbench.
 *
 * NO STYLING AND NO DESIGN SYSTEM, deliberately. The hosts do not agree on a
 * look — raglit's panes are plain CSS classes, caselit's app is MUI throughout —
 * and a reference UI in either idiom would force the other to adopt it, which is
 * how a shared component becomes a fourth implementation rather than replacing
 * three. This owns behaviour; every host draws its own.
 */
export * from './hooks.js'
export {
  KEYS,
  UNIT_VERDICTS,
  VERDICT_GLOSS,
  Workbench,
  complete,
  httpTransport,
  orphanCount,
  outstanding,
  provenance,
  ruled,
  textCorrected,
  untouched,
} from '@iodesystems/attest-core'
export type {
  Asset,
  AssetRef,
  Entry,
  Kind,
  Locator,
  Provenance,
  Rendering,
  State,
  Stats,
  Transport,
  Unit,
  UnitStatus,
  UnitVerdict,
  VerdictInput,
} from '@iodesystems/attest-core'
