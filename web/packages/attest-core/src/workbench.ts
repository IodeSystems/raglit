import type { Transport, VerdictInput } from './transport.js'
import type { State, UnitStatus, UnitVerdict } from './types.js'

/**
 * The part that is genuinely client-side: where the reviewer is, what they have
 * typed and not yet posted, and when that gets sent.
 *
 * Framework-agnostic on purpose. A React binding is thirty lines over this; so is
 * a Svelte one, and so is a test. Nothing in here touches the DOM.
 */

/**
 * A CORRECTION IS POSTED ON IDLE, ON BLUR AND ON UNLOAD — NEVER PER KEYSTROKE.
 *
 * This is where the design departs from oidio's, and the reason is the storage:
 * oidio rewrites a JSON file, so a write per keystroke costs nothing but IO. This
 * log is APPEND-ONLY — that is what lets two syncing machines merge without
 * losing a verdict — and a line per keystroke would bury one ruling under two
 * hundred, destroying the record it exists to keep.
 *
 * The trade is honest and worth stating: a hard kill inside this window loses the
 * last edit. Blur and unload close most of it.
 */
export const CORRECTION_IDLE_MS = 1500

/** What the workbench is showing and holding. */
export interface WorkbenchState {
  /** The resolved state from the server. Null until loaded. */
  state: State | null
  /** Index into `state.units`, in the server's order. */
  cursor: number
  /**
   * Edits typed and not yet posted, keyed by unit id. Held apart from `state` so
   * a refresh mid-typing cannot silently discard them, and so nothing renders a
   * pending edit as though it were a recorded ruling.
   */
  pending: Map<string, PendingEdit>
  error: string | null
  busy: boolean
}

export interface PendingEdit {
  text?: string
  label?: string
  note?: string
}

export interface WorkbenchOptions {
  transport: Transport
  assetId: string
  /** The self-declared person. Required, because a defaulted author reads afterwards exactly like a real one. */
  by: string
  /** Injected so tests do not wait, and so a host can tune it. */
  idleMs?: number
  now?: () => number
  setTimeoutImpl?: typeof setTimeout
  clearTimeoutImpl?: typeof clearTimeout
}

export type Listener = (s: WorkbenchState) => void

/**
 * The workbench.
 *
 * Deliberately NOT a store library and not reactive-framework-specific:
 * `subscribe` hands back a snapshot, which is all `useSyncExternalStore` and its
 * equivalents need.
 */
export class Workbench {
  private st: WorkbenchState = { state: null, cursor: 0, pending: new Map(), error: null, busy: false }
  private listeners = new Set<Listener>()
  private timer: ReturnType<typeof setTimeout> | null = null
  private readonly idleMs: number
  private readonly setT: typeof setTimeout
  private readonly clearT: typeof clearTimeout

  constructor(private readonly opts: WorkbenchOptions) {
    this.idleMs = opts.idleMs ?? CORRECTION_IDLE_MS
    this.setT = opts.setTimeoutImpl ?? setTimeout
    this.clearT = opts.clearTimeoutImpl ?? clearTimeout
  }

  snapshot(): WorkbenchState {
    return this.st
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }

  private emit(next: Partial<WorkbenchState>) {
    // A NEW OBJECT EVERY TIME, and a new Map when pending changed. A snapshot
    // mutated in place is invisible to every framework that compares references,
    // which shows up as a workbench that stops repainting after the first edit.
    this.st = { ...this.st, ...next }
    for (const fn of this.listeners) fn(this.st)
  }

  get current(): UnitStatus | null {
    const units = this.st.state?.units
    if (!units) return null
    return units[this.st.cursor] ?? null
  }

  async load(signal?: AbortSignal): Promise<void> {
    this.emit({ busy: true, error: null })
    try {
      const state = await this.opts.transport.state(this.opts.assetId, signal)
      // THE CURSOR IS CLAMPED, NOT RESET. A re-read that dropped units must not
      // throw a reviewer back to the top of a document they were halfway through;
      // landing them on the last unit that still exists is the closest true
      // position.
      const cursor = Math.min(this.st.cursor, Math.max(0, state.units.length - 1))
      this.emit({ state, cursor, busy: false })
    } catch (e) {
      this.emit({ busy: false, error: message(e) })
    }
  }

  /** Move without ruling. `j`/`k` in the keyboard flow. */
  move(delta: number): void {
    const n = this.st.state?.units.length ?? 0
    if (n === 0) return
    // FLUSH FIRST. Moving away from a unit is the same signal as blurring it: the
    // reviewer is done with it, and an unposted edit left behind is a correction
    // they believe they made.
    void this.flush()
    const cursor = Math.max(0, Math.min(n - 1, this.st.cursor + delta))
    this.emit({ cursor })
  }

  goto(index: number): void {
    const n = this.st.state?.units.length ?? 0
    if (n === 0) return
    void this.flush()
    this.emit({ cursor: Math.max(0, Math.min(n - 1, index)) })
  }

  /**
   * Type into the current unit. Schedules a post; does not send one.
   *
   * The edit lands in `pending` immediately so the UI can show it, and is not a
   * ruling until it posts. Nothing renders pending text as recorded.
   */
  edit(patch: PendingEdit): void {
    const u = this.current
    if (!u) return
    const pending = new Map(this.st.pending)
    pending.set(u.unit.id, { ...pending.get(u.unit.id), ...patch })
    this.emit({ pending })
    if (this.timer) this.clearT(this.timer)
    this.timer = this.setT(() => {
      this.timer = null
      void this.flush()
    }, this.idleMs)
  }

  /**
   * Post every pending edit as a `corrected` verdict.
   *
   * Call on blur and on unload as well as on the idle timer — those are the two
   * that close most of the window where a hard kill loses an edit.
   */
  async flush(): Promise<void> {
    if (this.timer) {
      this.clearT(this.timer)
      this.timer = null
    }
    if (this.st.pending.size === 0) return
    const pending = this.st.pending
    // Cleared BEFORE the awaits so a second flush racing this one does not post
    // the same correction twice. A failure puts them back.
    this.emit({ pending: new Map() })
    const failed = new Map<string, PendingEdit>()
    let err: string | null = null
    for (const [unit, edit] of pending) {
      try {
        await this.opts.transport.verdict({
          asset: this.opts.assetId,
          unit,
          kind: 'corrected',
          ...edit,
          by: this.opts.by,
        })
      } catch (e) {
        failed.set(unit, edit)
        err = message(e)
      }
    }
    if (failed.size > 0) {
      // PUT THEM BACK. A dropped correction is a reviewer's work silently
      // discarded, and they have no way to know it happened.
      this.emit({ pending: failed, error: err })
      return
    }
    await this.load()
  }

  /** Rule on the current unit and advance. */
  async rule(kind: UnitVerdict, extra?: { note?: string; text?: string; label?: string }): Promise<void> {
    const u = this.current
    if (!u) return
    await this.flush()
    const input: VerdictInput = {
      asset: this.opts.assetId,
      unit: u.unit.id,
      kind,
      by: this.opts.by,
      ...extra,
    }
    this.emit({ busy: true, error: null })
    try {
      await this.opts.transport.verdict(input)
      await this.load()
      this.move(1)
    } catch (e) {
      this.emit({ busy: false, error: message(e) })
    }
  }

  /** Take back the ruling on the current unit; it returns to untouched. */
  async retract(): Promise<void> {
    const u = this.current
    if (!u) return
    this.emit({ busy: true, error: null })
    try {
      await this.opts.transport.verdict({
        asset: this.opts.assetId,
        unit: u.unit.id,
        kind: 'retract',
        by: this.opts.by,
      })
      await this.load()
    } catch (e) {
      this.emit({ busy: false, error: message(e) })
    }
  }

  /**
   * The blanket affirmation.
   *
   * `statement` is the reviewer's own words and is NEVER generated here. A tool
   * that fills in "the rest is right" has changed what somebody attested to.
   */
  async sweep(statement?: string): Promise<void> {
    await this.flush()
    this.emit({ busy: true, error: null })
    try {
      await this.opts.transport.sweep({ asset: this.opts.assetId, by: this.opts.by, statement })
      await this.load()
    } catch (e) {
      this.emit({ busy: false, error: message(e) })
    }
  }
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/**
 * The default keyboard flow, as DATA rather than bound handlers.
 *
 * A host binds these to its own event target — the core has no business owning a
 * keydown listener — and a host with another medium adds its own without forking
 * the table. The audio keys (`space`, `r`) live in the host that has audio.
 */
export const KEYS: Record<string, { action: string; help: string }> = {
  j: { action: 'next', help: 'next claim, without ruling' },
  k: { action: 'prev', help: 'previous claim, without ruling' },
  c: { action: 'confirmed', help: 'confirm — something depends on this one' },
  a: { action: 'affirmed', help: 'affirm — read, nothing to change' },
  e: { action: 'edit', help: 'correct the text' },
  u: { action: 'unclear', help: 'cannot tell — a verdict on the artifact' },
  x: { action: 'unsupported', help: 'the asset is silent here' },
  d: { action: 'retract', help: 'take back the ruling' },
  A: { action: 'sweep', help: 'affirm everything read so far, under stated terms' },
  '?': { action: 'help', help: 'these keys' },
}
