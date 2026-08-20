/**
 * @iodesystems/attest-core — machine reading, human verdict, client half.
 *
 * An asset was read by a machine. The machine made claims about locatable pieces
 * of it. A person must be able to rule on those claims durably, with an honest
 * account of how much was actually ruled on. Only the LOCATOR varies.
 *
 * No framework, no DOM, and deliberately NO `resolve` — see state.ts.
 */
export * from './types.js'
export * from './transport.js'
export * from './state.js'
export * from './workbench.js'
