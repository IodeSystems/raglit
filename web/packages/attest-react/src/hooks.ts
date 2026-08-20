import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import {
  KEYS,
  Workbench,
  type AssetRef,
  type PendingEdit,
  type Transport,
  type UnitVerdict,
  type WorkbenchState,
} from '@iodesystems/attest-core'

/**
 * HEADLESS ON PURPOSE. No styling, no design system, no markup opinions.
 *
 * The two hosts this has to serve do not agree on a look: raglit's panes are
 * plain CSS classes over a shared stylesheet, caselit's app is MUI throughout.
 * A reference UI in either one would force the other to adopt it, which is how a
 * shared component becomes a fourth implementation instead of replacing three.
 *
 * So this layer owns the BEHAVIOUR — subscription, the keyboard flow, the
 * lifecycle that closes the window where an unposted correction is lost — and
 * every host draws its own workbench over it.
 */

/**
 * The workbench, bound to React.
 *
 * `useSyncExternalStore` rather than a `useState` mirror: the store is the source
 * of truth and copying it into component state is how a keystroke and a reload
 * race each other into a stale render.
 */
export function useWorkbench(opts: {
  transport: Transport
  assetId: string
  /** The self-declared person. A ruling with no author reads afterwards exactly like a real one. */
  by: string
  idleMs?: number
}) {
  const { transport, assetId, by, idleMs } = opts
  const wb = useMemo(
    () => new Workbench({ transport, assetId, by, idleMs }),
    [transport, assetId, by, idleMs],
  )

  const snapshot = useSyncExternalStore(
    useCallback((cb) => wb.subscribe(cb), [wb]),
    useCallback(() => wb.snapshot(), [wb]),
    useCallback(() => wb.snapshot(), [wb]),
  )

  useEffect(() => {
    const ac = new AbortController()
    void wb.load(ac.signal)
    return () => ac.abort()
  }, [wb])

  // THE TWO EVENTS THAT CLOSE THE LOSS WINDOW.
  //
  // A correction posts on an idle timer, never per keystroke, because the log is
  // append-only and a line per keystroke would bury one ruling under two hundred.
  // That leaves ~1.5s where a hard kill loses the last edit; unload and tab-hide
  // are what shrink it to almost nothing. `visibilitychange` and not `blur`
  // because a phone backgrounding the tab never fires blur, and the workbench is
  // usable on a phone.
  useEffect(() => {
    const flush = () => void wb.flush()
    const onHide = () => {
      if (document.visibilityState === 'hidden') flush()
    }
    window.addEventListener('beforeunload', flush)
    document.addEventListener('visibilitychange', onHide)
    return () => {
      window.removeEventListener('beforeunload', flush)
      document.removeEventListener('visibilitychange', onHide)
      // Unmounting is leaving too — a route change must not drop what was typed.
      flush()
    }
  }, [wb])

  return {
    ...snapshot,
    current: wb.current,
    workbench: wb,
    reload: useCallback(() => wb.load(), [wb]),
    move: useCallback((d: number) => wb.move(d), [wb]),
    goto: useCallback((i: number) => wb.goto(i), [wb]),
    edit: useCallback((p: PendingEdit) => wb.edit(p), [wb]),
    flush: useCallback(() => wb.flush(), [wb]),
    rule: useCallback(
      (k: UnitVerdict, extra?: { note?: string; text?: string; label?: string }) => wb.rule(k, extra),
      [wb],
    ),
    retract: useCallback(() => wb.retract(), [wb]),
    sweep: useCallback((statement?: string) => wb.sweep(statement), [wb]),
  } satisfies WorkbenchState & Record<string, unknown>
}

/** Everything reviewable in one mount, with each asset's completeness account. */
export function useAssets(transport: Transport) {
  const [assets, setAssets] = useState<AssetRef[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(() => {
    const ac = new AbortController()
    setError(null)
    transport
      .assets(ac.signal)
      .then(setAssets)
      .catch((e: unknown) => {
        if (ac.signal.aborted) return
        setError(e instanceof Error ? e.message : String(e))
      })
    return () => ac.abort()
  }, [transport])

  useEffect(() => reload(), [reload])
  return { assets, error, reload }
}

export type KeyAction = (typeof KEYS)[string]['action']

/**
 * The keyboard flow.
 *
 * A REVIEWER TYPING A CORRECTION IS NOT ISSUING COMMANDS. Without this guard `c`
 * inside a correction field confirms the unit and throws away what was being
 * typed — the single most destructive thing a workbench like this can do, and it
 * happens on the first character.
 */
export function useAttestKeys(
  wb: ReturnType<typeof useWorkbench>,
  opts?: { enabled?: boolean; onEdit?: () => void; onHelp?: () => void; onSweep?: () => void },
) {
  const enabled = opts?.enabled ?? true
  const cbs = useRef(opts)
  cbs.current = opts

  useEffect(() => {
    if (!enabled) return
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const t = e.target as HTMLElement | null
      if (t && (t.isContentEditable || /^(input|textarea|select)$/i.test(t.tagName))) return
      const hit = KEYS[e.key]
      if (!hit) return
      e.preventDefault()
      switch (hit.action) {
        case 'next':
          wb.move(1)
          break
        case 'prev':
          wb.move(-1)
          break
        case 'edit':
          cbs.current?.onEdit?.()
          break
        case 'help':
          cbs.current?.onHelp?.()
          break
        case 'sweep':
          // NEVER fired directly. A blanket affirmation carries the reviewer's own
          // terms, and a keystroke that swept without asking for them would record
          // a qualified claim nobody made.
          cbs.current?.onSweep?.()
          break
        case 'retract':
          void wb.retract()
          break
        default:
          void wb.rule(hit.action as UnitVerdict)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [wb, enabled])

  return KEYS
}
