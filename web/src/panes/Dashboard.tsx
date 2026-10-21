import { Link, useParams } from "@tanstack/react-router";

import {
  getJSON,
  getStatus,
  listChannels,
  listIdentityJobs,
  listJobs,
  type IdentityQueueStatus,
  type IndexInfo,
  type Job,
  type LaneStatus,
  type ModelChannel,
  type StatusSnapshot,
  type TagCount,
} from "../api";
import { usePoll } from "../usePoll";
import { ErrorText, Details } from "../ui/Details";
import { AISummaryLabel } from "../ui/Label";
import { displayPath, documents, kindLabel, laneLabel, passages } from "../ui/display";
import { splitName } from "../shell/IndexShell";
import { useIndexInfo } from "./IndexRows";
import "./scope.css";

// The index overview: what this index holds, what needs attention, and what it
// did lately.
//
// It used to be seven stat cards and nothing else — sizes and queue counters,
// which told a reader neither what kind of corpus this is nor whether anything
// was wrong. The digest the daemon keeps (/indexes: about, kinds, content and
// role tags) answers the first; /api/problems and the two job queues the
// second and third.
//
// Polled rather than pushed. An import run is the thing people sit in front of
// this for, and a static snapshot of a queue that is draining tells them nothing.
export function Dashboard() {
  const { index } = useParams({ from: "/i/$index" });
  const { data, error } = usePoll<StatusSnapshot>(() => getStatus(index), 3000, [index]);
  const info = useIndexInfo().get(index);

  if (error) {
    return (
      <div className="empty">
        <ErrorText raw={error} />
      </div>
    );
  }
  if (!data) return <div className="empty">Loading…</div>;

  const { project, local } = splitName(index);
  const busy = (data.pending ?? 0) + (data.running ?? 0);

  return (
    <>
      <div className="scopehead">
        <h1>
          {local}
          {project && <span className="muted"> · {project}</span>}
        </h1>
        <div className="facts">
          {documents(data.documents ?? 0)}
          <span className="sep">·</span>
          {passages(data.fragments ?? 0)}
          <span className="sep">·</span>
          {busy > 0
            ? `importing: ${(data.running ?? 0).toLocaleString()} in progress, ${(
                data.pending ?? 0
              ).toLocaleString()} waiting`
            : "no imports running"}
        </div>
        {info?.about && (
          <div className="about">
            <AISummaryLabel />
            {info.about}
            {info.about_stale && (
              <div className="muted small">
                Written before the latest imports, so it may be out of date.
              </div>
            )}
          </div>
        )}
      </div>

      <div className="scopegrid">
        <Holds index={index} info={info} />
        <Attention index={index} failedImports={data.failed ?? 0} />
      </div>

      <Recent index={index} root={info?.root} />
      <Lanes lanes={data.lanes} />
      <Channels />
    </>
  );
}

// ── what it holds ───────────────────────────────────────────────────────────

function Holds({ index, info }: { index: string; info?: IndexInfo }) {
  const kinds = info?.kinds ?? [];
  const top = kinds.length ? Math.max(...kinds.map((k) => k.count)) : 0;
  return (
    <section className="panel">
      <h3>What it holds</h3>
      {!kinds.length && (
        <p className="muted small">
          No document types yet. They appear once documents have been described.
        </p>
      )}
      {!!kinds.length && (
        <div className="kindbars">
          {kinds.map((k) => (
            <KindBar key={k.tag} k={k} top={top} />
          ))}
        </div>
      )}
      <Tags title="Topics" tags={info?.content} />
      <Tags title="Roles" tags={info?.roles} />
      <p className="small" style={{ marginBottom: 0 }}>
        <Link className="scopelink" to="/i/$index/d" params={{ index }}>
          All documents →
        </Link>{" "}
        <Link className="scopelink" to="/i/$index/types" params={{ index }} style={{ marginLeft: 12 }}>
          Types &amp; fields →
        </Link>
      </p>
    </section>
  );
}

function KindBar({ k, top }: { k: TagCount; top: number }) {
  return (
    <>
      <span>{kindLabel(k.tag)}</span>
      <span className="bar" aria-hidden>
        <span style={{ width: `${top ? Math.max(2, (k.count / top) * 100) : 0}%` }} />
      </span>
      <span className="n">{k.count.toLocaleString()}</span>
    </>
  );
}

function Tags({ title, tags }: { title: string; tags?: TagCount[] }) {
  if (!tags?.length) return null;
  return (
    <div style={{ marginTop: 16 }}>
      <div className="muted small" style={{ marginBottom: 4 }}>
        {title}
      </div>
      <div className="tagchips">
        {tags.slice(0, 12).map((t) => (
          <span className="kindchip" key={t.tag}>
            {t.tag} <span className="muted">{t.count.toLocaleString()}</span>
          </span>
        ))}
      </div>
    </div>
  );
}

// ── needs attention ─────────────────────────────────────────────────────────

// Short plain names for the problem kinds the Problems page explains in full.
// A kind not listed here is still counted, under its own name, so a newer
// daemon's problem is not dropped from the summary.
const PROBLEM_LABELS: Record<string, string> = {
  "no-fragments": "documents that cannot be found by search",
  "generated-indexed": "raglit output files imported as documents",
  "missing-file": "documents whose file has moved or is missing",
  "no-pages": "documents with no pages recorded",
  "job-failed": "imports that failed",
  "segment-degraded": "documents stored whole, not split into passages",
  "empty-source": "empty files",
  "page-unread": "documents with pages that could not be read",
  "page-partial": "documents with pages only partly read",
  "llm-retries": "imports that needed retries",
};

function Attention({ index, failedImports }: { index: string; failedImports: number }) {
  const { data: probs, error } = usePoll<{ problems?: { kind: string }[] }>(
    () => getJSON("/api/problems", { index }),
    15000,
    [index],
  );
  const { data: ident } = usePoll<{ queue: IdentityQueueStatus }>(
    () => listIdentityJobs(index, "error", 1),
    10000,
    [index],
  );

  const byKind = new Map<string, number>();
  for (const p of probs?.problems ?? []) {
    if (p.kind === "withdrawn") continue;
    byKind.set(p.kind, (byKind.get(p.kind) ?? 0) + 1);
  }
  // The Problems page count, before the addition below, so the link's number
  // matches the page it opens and the sidebar badge.
  const total = [...byKind.values()].reduce((n, c) => n + c, 0);
  // /status counts failed import jobs even when /api/problems does not list
  // them; show the count rather than neither.
  if (probs && failedImports > 0 && !byKind.has("job-failed")) byKind.set("job-failed", failedImports);
  const rows = [...byKind.entries()].sort((a, b) => b[1] - a[1]);
  const captionFailed = ident?.queue?.failed ?? 0;

  return (
    <section className="panel">
      <h3>Needs attention</h3>
      {error && <ErrorText raw={error} />}
      {!probs && !error && <p className="muted small">Checking…</p>}
      {probs && !total && !captionFailed && !failedImports && (
        <p className="okline">Nothing needs attention.</p>
      )}
      {(!!rows.length || !!captionFailed) && (
        <ul className="attn">
          {rows.map(([kind, n]) => (
            <li key={kind}>
              <span className="n bad">{n.toLocaleString()}</span>
              <span className="what">{PROBLEM_LABELS[kind] ?? kind.replace(/-/g, " ")}</span>
            </li>
          ))}
          {!!captionFailed && (
            <li>
              <span className="n bad">{captionFailed.toLocaleString()}</span>
              <span className="what">
                documents the AI could not describe{" "}
                <Link className="scopelink" to="/i/$index/activity" params={{ index }}>
                  Activity →
                </Link>
              </span>
            </li>
          )}
        </ul>
      )}
      {!!total && (
        <p className="small" style={{ marginBottom: 0 }}>
          <Link className="scopelink" to="/i/$index/health" params={{ index }}>
            See all {total.toLocaleString()} {total === 1 ? "problem" : "problems"} →
          </Link>
        </p>
      )}
    </section>
  );
}

// ── recent activity ─────────────────────────────────────────────────────────

const JOB_STATE: Record<string, [label: string, cls: string]> = {
  done: ["Imported", "done"],
  error: ["Failed", "error"],
  failed: ["Failed", "error"],
  running: ["Importing", "running"],
  pending: ["Waiting", "pending"],
};

function Recent({ index, root }: { index: string; root?: string }) {
  const { data } = usePoll<{ jobs: Job[] }>(() => listJobs(index, 6), 5000, [index]);
  const jobs = data?.jobs ?? [];
  if (!jobs.length) return null;

  return (
    <>
      <h2 className="scopesec">
        Recent imports
        <Link className="more" to="/i/$index/jobs" params={{ index }}>
          All import jobs →
        </Link>
      </h2>
      <div className="panel" style={{ padding: "12px 16px" }}>
        <ul className="recent">
          {jobs.map((j) => {
            const [label, cls] = JOB_STATE[j.state] ?? [j.state, ""];
            const at = j.finished_at || j.started_at || j.enqueued_at;
            return (
              <li key={j.id}>
                <span className={"badge " + cls}>{label}</span>
                <Link
                  className="what scopelink"
                  to="/i/$index/jobs/$jobId"
                  params={{ index, jobId: String(j.id) }}
                  title={displayPath(j.url, root)}
                >
                  {displayPath(j.url, root)}
                </Link>
                {at ? <span className="when">{ago(at)}</span> : null}
              </li>
            );
          })}
        </ul>
      </div>
    </>
  );
}

// Job timestamps are unix NANOseconds. Relative for the last day, a date after.
function ago(ns: number): string {
  const ms = ns > 1e15 ? ns / 1e6 : ns > 1e12 ? ns : ns * 1000;
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return new Date(ms).toLocaleDateString();
}

// ── work in progress ────────────────────────────────────────────────────────

// The queue by size of work. "12 waiting" is an afternoon if they are large
// scans and under a minute if they are small text files, so the split is shown.
function Lanes({ lanes }: { lanes?: Record<string, LaneStatus> }) {
  const rows = Object.entries(lanes ?? {}).filter(
    ([, l]) => (l.pending ?? 0) + (l.running ?? 0) > 0,
  );
  if (!rows.length) return null;
  rows.sort(([a], [b]) => (a === "heavy" ? -1 : b === "heavy" ? 1 : a.localeCompare(b)));

  return (
    <>
      <h2 className="scopesec">Imports in progress</h2>
      <div className="panel" style={{ padding: "12px 16px" }}>
        <ul className="recent">
          {rows.map(([name, l]) => (
            <li key={name}>
              <span className={"badge lane-" + name}>{laneLabel(name)} files</span>
              <span className="what muted">
                {(l.running ?? 0).toLocaleString()} in progress
                {l.pending ? ` · ${l.pending.toLocaleString()} waiting` : ""}
              </span>
            </li>
          ))}
        </ul>
      </div>
    </>
  );
}

// Per-model capacity, which is what actually bounds an import. Operational
// detail, so it sits in a collapsed disclosure: a reader of the corpus does not
// need it, whoever is watching an import run does.
//
// The width is learned from the server's own backpressure rather than
// configured: it starts at one and grows while calls succeed, and a "busy"
// reply halves it, because the server's capacity is shared with other traffic.
function Channels() {
  const { data } = usePoll<{ channels?: ModelChannel[] }>(listChannels, 3000, []);
  const chans = data?.channels ?? [];
  if (!chans.length) return null;

  return (
    <div style={{ marginTop: 24 }}>
      <Details summary="AI model capacity">
        <p className="muted small" style={{ marginTop: 0 }}>
          Each AI model takes its own number of requests at once. The number
          grows while requests succeed and halves when the model replies that
          it is busy.
        </p>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Model</th>
                <th className="num">At once</th>
                <th className="num">In progress</th>
                <th className="num">Most at once</th>
                <th className="num">Requests</th>
                <th className="num">Busy replies</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {chans.map((c) => (
                <tr key={c.model}>
                  <td>{c.model}</td>
                  <td className="num">{c.width ?? 1}</td>
                  <td className="num">{c.in_flight ?? 0}</td>
                  <td className="num">{c.peak ?? 0}</td>
                  <td className="num">{(c.calls ?? 0).toLocaleString()}</td>
                  <td className={"num " + (c.n_429 ? "err" : "muted")}>
                    {(c.n_429 ?? 0).toLocaleString()}
                  </td>
                  <td>
                    {/* Slowing down is not an error: the model is being polite
                        to whoever else is using it. Saying so stops it reading
                        as a stall. */}
                    {c.chilled && (
                      <span className="badge warn" title="Held low after busy replies; it will rise again">
                        slowing down
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Details>
    </div>
  );
}
