import type { AssetRef, Kind, State, Unit } from './types.js'

/**
 * The six operations the workbench needs, as an interface a host implements.
 *
 * THE HOST OWNS AUTH ENTIRELY, which is why this is injected rather than a
 * fetch wrapper with a base URL. `attest` authenticates nobody: raglit mounts the
 * service on its own router behind its own middleware, caselit mounts it behind
 * `requireRole` and a per-case grant, and the CLI runs as an authorized guest on
 * loopback. A transport that built its own requests would have to know which of
 * those it was inside.
 *
 * It is also what keeps this package honest about the one thing it must not do:
 * there is no `resolve` here. See state.ts.
 */
export interface Transport {
  /** Everything reviewable, with each asset's completeness account if the host has it. */
  assets(signal?: AbortSignal): Promise<AssetRef[]>

  /** The reading and its verdicts, RESOLVED SERVER-SIDE. */
  state(assetId: string, signal?: AbortSignal): Promise<State>

  /** Rule on one unit. `by` is the person; the host supplies the account. */
  verdict(input: VerdictInput, signal?: AbortSignal): Promise<{ stats?: unknown }>

  /**
   * A blanket affirmation: one line covering every unit with no ruling EARLIER IN
   * THE LOG. One line and not one per unit, so the sweep keeps its identity — who
   * swept and when is a single fact about the pass.
   */
  sweep(input: SweepInput, signal?: AbortSignal): Promise<{ stats?: unknown }>

  /** Replace units with the person's own decomposition. */
  resegment(input: ResegmentInput, signal?: AbortSignal): Promise<{ stats?: unknown }>

  /**
   * Where the reviewer looks: the crop, the audio window, the sentence. A URL
   * rather than bytes — an <img src> or an <audio src> must be able to use it, and
   * audio review needs the browser to answer its own Range requests.
   */
  evidenceURL(assetId: string, unitId: string, rendering?: Rendering): string
}

/**
 * Three renderings, because they answer three different questions and ONLY THE
 * CROP IS ATTESTABLE.
 *
 * `crop` is the exact artifact the claim was read from and is what a verdict
 * rests on. `asSeen` re-renders it now, which is what catches a page that changed
 * underneath a ruling. `humane` is the version a person can actually use — level-
 * corrected audio, a legible upscale — and a verdict against it cannot say which
 * artifact it rests on, which is the gap that made oidio's own passes ambiguous.
 */
export type Rendering = 'crop' | 'asSeen' | 'humane'

export interface VerdictInput {
  asset: string
  unit: string
  kind: Extract<Kind, 'confirmed' | 'corrected' | 'affirmed' | 'unclear' | 'unsupported' | 'retract'>
  /** The correction. Omitted means NOT DISPUTED — never "cleared to empty". */
  text?: string
  label?: string
  note?: string
  by: string
}

export interface SweepInput {
  asset: string
  by: string
  /**
   * The reviewer's own words, quoted verbatim downstream and never summarised.
   * An affirmation is a qualified claim with a materiality standard inside it,
   * and "the rest is right" is a different assertion from the one they made.
   */
  statement?: string
}

export interface ResegmentInput {
  asset: string
  by: string
  units: Unit[]
  supersedes: string[]
  note?: string
}

/**
 * A Transport over a host's own HTTP surface, for hosts that mount the standard
 * routes and want nothing custom.
 *
 * `fetchImpl` is injectable so a host can hand in one that carries its cookies,
 * retries, or a case id — and so this is testable without a server.
 */
export function httpTransport(opts: {
  /** Where the service is mounted, e.g. `/api/attest/delano__default`. No trailing slash. */
  prefix: string
  fetchImpl?: typeof fetch
}): Transport {
  const f = opts.fetchImpl ?? fetch
  const base = opts.prefix.replace(/\/$/, '')

  async function json<T>(path: string, init?: RequestInit): Promise<T> {
    const res = await f(base + path, {
      ...init,
      headers: { 'content-type': 'application/json', ...(init?.headers ?? {}) },
    })
    if (!res.ok) {
      // The body first: this service answers with a problem document that says
      // which unit and why, and swallowing it for a status code is how a reviewer
      // gets "500" for "you did not say who you are".
      const detail = await res.text().catch(() => '')
      throw new Error(detail || `${res.status} ${res.statusText}`)
    }
    return (await res.json()) as T
  }

  return {
    assets: (signal) => json<{ assets?: AssetRef[] }>('/assets', { signal }).then((r) => r.assets ?? []),
    state: (assetId, signal) =>
      json<State>(`/state?asset=${encodeURIComponent(assetId)}`, { signal }),
    verdict: (input, signal) =>
      json('/verdict', { method: 'POST', body: JSON.stringify(input), signal }),
    sweep: (input, signal) => json('/sweep', { method: 'POST', body: JSON.stringify(input), signal }),
    resegment: (input, signal) =>
      json('/resegment', { method: 'POST', body: JSON.stringify(input), signal }),
    evidenceURL: (assetId, unitId, rendering) => {
      const q = new URLSearchParams({ asset: assetId, unit: unitId })
      if (rendering) q.set('rendering', rendering)
      return `${base}/evidence?${q.toString()}`
    },
  }
}
