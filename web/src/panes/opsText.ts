import { useEffect, useState } from "react";

import { listIndexes, postJSON } from "../api";
import { displayPath, engineLabel } from "../ui/display";

// Shared wording for the three operations panes (Activity, Jobs, Health): how a
// queue state, a pipeline stage and a retry tally read to a person. The general
// decisions (paths, errors, engines, lanes) live in ui/display.ts; this file is
// only what those three panes say and nobody else does.

// The index root, from /indexes. The queue endpoints (/api/jobs, /api/problems,
// /api/identity-jobs) send absolute paths, so the pane has to make them
// root-relative itself. `ready` is false until /indexes answered, so a link is
// not built from a half-known path.
export function useIndexRoot(index: string): { root?: string; ready: boolean } {
  const [got, setGot] = useState<{ index: string; root?: string } | null>(null);
  useEffect(() => {
    let live = true;
    listIndexes()
      .then((r) => live && setGot({ index, root: r.indexes?.find((i) => i.name === index)?.root }))
      .catch(() => live && setGot({ index }));
    return () => {
      live = false;
    };
  }, [index]);
  return { root: got?.index === index ? got.root : undefined, ready: got?.index === index };
}

// raglit's own storage: ~/.raglit/indexes/<name>/attachments/… and uploads/….
// The daemon spells those "@attachments/…" (docpaths.go StorageRel), and accepts
// that spelling on every document endpoint.
const STORAGE = /\/indexes\/[^/]+\/((?:attachments|uploads)\/.+)$/;

// docRel is the display path the daemon accepts back: root-relative, or "@…"
// for raglit's storage. It is used both as the visible text and as the route
// param, so the absolute path appears in neither.
export function docRel(path: string, root?: string): string {
  if (!path) return "";
  if (root) {
    const r = root.replace(/\/+$/, "");
    if (path.startsWith(r + "/")) return path.slice(r.length + 1);
  }
  const m = path.match(STORAGE);
  if (m) return "@" + m[1];
  return displayPath(path, root);
}

// A route param for a document link. Only a root-relative or "@…" path resolves
// on the daemon; displayPath's "…/a/b/c" fallback does not, so when the root is
// unknown the indexed path is used (the middleware passes it through).
export function docParam(path: string, root?: string): string {
  const rel = docRel(path, root);
  return rel.startsWith("…/") ? path : rel;
}

const STATES: Record<string, string> = {
  pending: "Waiting",
  running: "Running",
  done: "Done",
  error: "Failed",
  skipped: "Skipped",
  skip: "Skipped",
  warn: "Warning",
  degraded: "Partial",
  cancelled: "Cancelled",
  canceled: "Cancelled",
};

export function stateLabel(state: string): string {
  return STATES[state] ?? cap(state);
}

// Pipeline stage names (stages.go, worker.go, pipeline.go) in plain words.
const STAGES: Record<string, string> = {
  fetch: "Fetch file",
  extract: "Extract text",
  ocr: "Read scanned pages",
  reading: "Read pages",
  segment: "Split into passages",
  embed: "Index for meaning search",
  commit: "Save",
  attachments: "Attachments",
  flatten: "Flatten",
  described: "Describe",
  reconcile: "Match moved file",
  withdrawn: "Withdrawn",
  lane: "Queue size",
  "llm-retries": "AI service retries",
};

export function stageLabel(name?: string): string {
  if (!name) return "";
  return STAGES[name] ?? cap(name.replace(/[-_]/g, " "));
}

// Identity-queue modes (identityqueue.go).
const ASKS: Record<string, string> = {
  identity: "Title and summary",
  tags: "Tags",
  fields: "Fields",
};

export function askLabel(mode?: string): string {
  if (!mode) return "";
  return ASKS[mode] ?? cap(mode);
}

// Ingest segmentation modes (queue.go JobInfo.Mode).
const SEG_MODES: Record<string, string> = { llm: "AI-segmented", offline: "Text" };

export function segModeLabel(mode?: string): string {
  if (!mode) return "";
  return SEG_MODES[mode] ?? engineLabel(mode);
}

// "vision×2, tesseract×1" (stages.go engineSummary) → "Scanned (AI) ×2, …".
// A single engine name goes through engineLabel.
export function engineText(engine?: string): string {
  if (!engine) return "";
  return engine
    .split(/,\s*/)
    .map((part) => {
      const m = part.match(/^(.+?)×(\d+)$/);
      return m ? `${engineLabel(m[1])} ×${m[2]}` : engineLabel(part);
    })
    .join(", ");
}

const n = (v: string, one: string, many: string) => `${v} ${v === "1" ? one : many}`;

// retryText rewrites a RetrySummary.Detail (retrytally.go) into words:
//   "11×429 (up to 3 ahead in queue), 2×5xx, 11 recovered; 2m0s waiting, worst
//    call 40s; last: 429 {…}"
// → "11 rate-limited retries (up to 3 requests ahead in the queue), 2 server
//    errors, 11 recovered; 2m0s waiting, slowest call 40s"
// The "last:" response body is dropped here; the raw text stays in the
// Technical details disclosure.
export function retryText(detail?: string): string {
  if (!detail) return "";
  return detail
    .replace(/;\s*last:.*$/s, "")
    .replace(/(\d+)×429/g, (_, v: string) => n(v, "rate-limited retry", "rate-limited retries"))
    .replace(/\(up to (\d+) ahead in queue\)/g, "(up to $1 requests ahead in the queue)")
    .replace(/(\d+)×5xx/g, (_, v: string) => n(v, "server error", "server errors"))
    .replace(/(\d+)×unreachable/g, (_, v: string) => n(v, "connection failure", "connection failures"))
    .replace(/(\d+) gave up/g, (_, v: string) => `${v} failed after all retries`)
    .replace(/worst call/g, "slowest call")
    .trim();
}

// True when the text is a retry tally, so it is shown as one rather than as an
// error to humanize.
export function isRetryTally(detail?: string): boolean {
  return !!detail && /\d+×(?:429|5xx|unreachable)/.test(detail);
}

// Retry many items with a small number of requests in flight, so a "Retry all"
// over hundreds of rows neither floods the daemon nor takes one round-trip per
// row in sequence. Returns how many failed.
export async function retryEach<T>(items: T[], post: (item: T) => Promise<unknown>, width = 4): Promise<number> {
  let next = 0;
  let failed = 0;
  const lane = async () => {
    while (next < items.length) {
      const item = items[next++]!;
      try {
        await post(item);
      } catch {
        failed++;
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(width, items.length) }, lane));
  return failed;
}

export const retryJob = (index: string, id: number) => postJSON("/api/jobs/retry", { index }, { id });

function cap(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}
