import type { AssetRef, State, Transport } from '@iodesystems/attest-react'

import { getJSON, postJSON } from '../api'

/**
 * raglit's Transport for the attest workbench.
 *
 * THIS IS THE INJECTION POINT WORKING. The core ships an `httpTransport` that
 * would do most of this, and it is not used here for one concrete reason: it
 * throws a plain Error, and this pane has to tell a 404 apart from every other
 * failure. A 404 from the mount means NOTHING HAS READ THIS DOCUMENT — a state
 * with an action, not a broken server — and a reader shown a generic error has
 * no way to tell those apart and every reason to assume the second.
 *
 * So raglit passes its own `getJSON`, which carries the status on an `ApiError`,
 * along with whatever else that helper does about credentials and base paths. A
 * transport that built its own requests could not have known any of it.
 */
export function raglitTransport(index: string): Transport {
  const base = `/api/attest/${encodeURIComponent(index)}`
  return {
    assets: () => getJSON<{ assets?: AssetRef[] }>(`${base}/assets`).then((r) => r.assets ?? []),
    state: (asset) => getJSON<State>(`${base}/state`, { asset }),
    verdict: (input) => postJSON(`${base}/verdict`, undefined, input),
    sweep: (input) => postJSON(`${base}/sweep`, undefined, input),
    resegment: (input) => postJSON(`${base}/resegment`, undefined, input),
    evidenceURL: (asset, unit, rendering) => {
      const q = new URLSearchParams({ asset, unit })
      if (rendering) q.set('rendering', rendering)
      return `${base}/evidence?${q.toString()}`
    },
  }
}
