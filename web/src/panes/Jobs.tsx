import { useParams } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { listJobs, postJSON, type Job } from "../api";
import { usePoll } from "../usePoll";

// The ingest queue, and the three things you can do to a row.
//
// /jobs/:id focuses one. A health row links AT a failure rather than at the tab,
// because a link that lands on two hundred rows leaves the reader searching by
// eye for the one the link was about.
export function Jobs() {
  const { index } = useParams({ from: "/i/$index" });
  const params = useParams({ strict: false }) as { jobId?: string };
  const focus = params.jobId ? Number(params.jobId) : 0;
  const { data, error, refresh } = usePoll(() => listJobs(index), 3000, [index]);
  const [busy, setBusy] = useState<number | null>(null);
  const rowRef = useRef<HTMLTableSectionElement>(null);

  useEffect(() => {
    if (!focus) return;
    rowRef.current
      ?.querySelector(`[data-job="${focus}"]`)
      ?.scrollIntoView({ block: "center" });
  }, [focus, data]);

  const act = async (kind: "retry" | "cancel" | "forget", id: number) => {
    setBusy(id);
    try {
      await postJSON(`/api/jobs/${kind}`, { index }, { id });
      refresh();
    } finally {
      setBusy(null);
    }
  };

  if (error) return <div className="empty err">{error}</div>;
  if (!data) return <div className="empty">loading…</div>;
  const jobs = data.jobs ?? [];
  if (!jobs.length) return <div className="empty">No jobs.</div>;

  return (
    <div className="panel">
      <table>
        <thead>
          <tr>
            <th>#</th>
            <th>State</th>
            <th>Mode</th>
            <th>Target</th>
            <th className="num">Frags</th>
            <th>ETA</th>
            <th>Detail</th>
            <th />
          </tr>
        </thead>
        <tbody ref={rowRef}>
          {jobs.map((j) => (
            <tr key={j.id} data-job={j.id} className={focus === j.id ? "focus" : undefined}>
              <td>{j.id}</td>
              <td>
                <span className={"badge " + j.state}>{j.state}</span>
              </td>
              <td>{j.mode}</td>
              <td className="url" title={j.target}>
                {j.target}
              </td>
              <td className="num">{j.fragments ?? ""}</td>
              <td>{eta(j.eta_seconds)}</td>
              <td className={j.error ? "err" : "muted"}>{j.error || j.detail || ""}</td>
              <td className="actions">
                <JobActions job={j} busy={busy === j.id} act={act} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Which actions a row offers is a function of its state, and the daemon enforces
// the same rule with a 400. Offering a button that cannot work teaches people to
// ignore errors.
function JobActions({
  job,
  busy,
  act,
}: {
  job: Job;
  busy: boolean;
  act: (kind: "retry" | "cancel" | "forget", id: number) => void;
}) {
  const terminal = job.state === "error" || job.state === "done";
  return (
    <>
      {terminal && (
        <button disabled={busy} onClick={() => act("retry", job.id)}>
          Retry
        </button>
      )}
      {job.state === "pending" && (
        <button disabled={busy} onClick={() => act("cancel", job.id)}>
          Cancel
        </button>
      )}
      {terminal && (
        <button disabled={busy} onClick={() => act("forget", job.id)}>
          Forget
        </button>
      )}
    </>
  );
}

function eta(s?: number): string {
  if (!s) return "";
  return s < 90 ? `~${Math.round(s)}s` : `~${Math.round(s / 60)}m`;
}
