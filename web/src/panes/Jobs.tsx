import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";

import { getJob, listJobs, postJSON, type Job, type JobStage } from "../api";
import { usePoll } from "../usePoll";

// The ingest queue, and the three things you can do to a row.
//
// /jobs/:id opens ONE job in full — its stages, its timings, what each stage
// said. It does not merely highlight a row in the list, because the list is the
// newest 200 and a cited job is routinely older than that: the health report
// linked at job 927 while the newest was 1467, so the link landed on a table
// that did not contain it and appeared to do nothing at all.
export function Jobs() {
  const { index } = useParams({ from: "/i/$index" });
  const params = useParams({ strict: false }) as { jobId?: string };
  const focus = params.jobId ? Number(params.jobId) : 0;
  if (focus) return <JobDetail index={index} id={focus} />;
  return <JobList index={index} />;
}

function JobList({ index }: { index: string }) {
  const { data, error, refresh } = usePoll(() => listJobs(index), 3000, [index]);
  const [busy, setBusy] = useState<number | null>(null);

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
            <th>Lane</th>
            <th>Mode</th>
            <th>Target</th>
            <th className="num">Frags</th>
            <th>Took</th>
            <th>Detail</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {jobs.map((j) => (
            <tr key={j.id} data-job={j.id}>
              <td>
                <Link to="/i/$index/jobs/$jobId" params={{ index, jobId: String(j.id) }}>
                  {j.id}
                </Link>
              </td>
              <td>
                <span className={"badge " + j.state}>{j.state}</span>
              </td>
              <td>{j.lane && <span className={"badge lane-" + j.lane}>{j.lane}</span>}</td>
              <td>{j.mode}</td>
              <td className="url" title={j.url}>
                {j.url}
              </td>
              <td className="num">{j.fragments ?? ""}</td>
              <td>{j.state === "pending" || j.state === "running" ? eta(j.eta_seconds) : took(j)}</td>
              <td className={j.error ? "err" : "muted"}>{j.error || lastStage(j)}</td>
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

// One job, fetched BY ID so its age does not matter, with every stage the
// pipeline recorded. The daemon has always returned these; nothing rendered
// them, which is why a health row could name a bad ingest and lead nowhere.
function JobDetail({ index, id }: { index: string; id: number }) {
  const { data, error, refresh } = usePoll<Job | undefined>(
    () => getJob(index, id),
    3000,
    [index, id],
  );
  const [busy, setBusy] = useState(false);
  const [actErr, setActErr] = useState("");

  const act = async (kind: "retry" | "cancel" | "forget") => {
    setBusy(true);
    setActErr("");
    try {
      await postJSON(`/api/jobs/${kind}`, { index }, { id });
      refresh();
    } catch (e: unknown) {
      setActErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (error) return <div className="empty err">{error}</div>;
  if (!data) return <div className="empty">loading…</div>;
  const j = data;
  const stages = j.stages ?? [];

  return (
    <>
      <div className="panel jobhead">
        <div className="subj">
          <span className="badge">#{j.id}</span>{" "}
          <span className={"badge " + j.state}>{j.state}</span>{" "}
          {j.lane && <span className={"badge lane-" + j.lane}>{j.lane}</span>}{" "}
          {j.mode && <span className="badge">{j.mode}</span>}{" "}
          <Link to="/i/$index/d/$doc/pages" params={{ index, doc: j.url }}>
            {j.url}
          </Link>
        </div>
        <div className="muted">
          {j.fragments ? `${j.fragments} fragment(s) · ` : ""}
          queued {when(j.enqueued_at)}
          {j.started_at ? ` · started ${when(j.started_at)}` : ""}
          {j.finished_at ? ` · took ${took(j)}` : ""}
        </div>
        {j.error && <div className="err">{j.error}</div>}
        <div className="acts">
          <JobActions job={j} busy={busy} act={(k) => act(k)} />
          <Link className="act" to="/i/$index/jobs" params={{ index }}>
            All jobs
          </Link>
        </div>
        {actErr && <div className="err">{actErr}</div>}
      </div>

      {!stages.length ? (
        <div className="panel">
          <h3>Stages</h3>
          <p className="why">
            No stages recorded. A job enqueued before stage logging, or one that
            never started.
          </p>
        </div>
      ) : (
        runsOf(stages).map((run, i, all) => (
          <div className="panel probgroup" key={run[0]!.id}>
            <h3>
              {all.length > 1 ? `Attempt ${i + 1} of ${all.length}` : "Stages"}
              {run[0]?.at ? <span className="muted"> · {when(run[0].at)}</span> : null}
            </h3>
            <table>
              <thead>
                <tr>
                  <th>#</th>
                  <th>Stage</th>
                  <th>Engine</th>
                  <th>State</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {run.map((s) => (
                  <tr key={s.id}>
                    <td>{s.seq}</td>
                    <td>{s.name}</td>
                    <td>{s.engine}</td>
                    <td>
                      <span className={"badge " + stageClass(s.state)}>{s.state}</span>
                    </td>
                    {/* Verbatim, and wrapped rather than truncated. A stage detail
                        is the endpoint's own words — the repetition-guard message
                        names the block it looped on, and summarising it is how a
                        cause becomes "status 500". */}
                    <td className="det">{s.detail}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))
      )}
    </>
  );
}

// Split a job's stages into its RUNS.
//
// A retry appends a whole second pipeline to the same job, with seq restarting
// at 1 — so a job retried four times carries four fetch stages, four extracts,
// and four different segment outcomes under one id. Job 927 on the delano index
// is exactly that, and rendered flat it reads as "fetch, fetch, fetch, fetch,
// extract, extract, …": every fact present and no way to tell which failure
// belonged to which attempt.
//
// Rows arrive in insertion order (ListJobStages orders by id), so a run ends
// wherever seq stops increasing.
function runsOf(stages: JobStage[]): JobStage[][] {
  const runs: JobStage[][] = [];
  for (const s of stages) {
    const cur = runs[runs.length - 1];
    if (!cur || s.seq <= cur[cur.length - 1]!.seq) runs.push([s]);
    else cur.push(s);
  }
  return runs;
}

// A warn is not an error and must not read as one: the llm-retries stage is a
// job that COMPLETED after the endpoint made it fight, which is a warning that a
// failure is coming, not a failure.
function stageClass(state: string): string {
  return state === "error" ? "error" : state === "warn" ? "warn" : state;
}

function lastStage(j: Job): string {
  const s = j.stages;
  if (!s?.length) return "";
  const bad = s.filter((x) => x.state === "error" || x.state === "warn").pop();
  return (bad ?? s[s.length - 1]!).detail ?? "";
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

// Timestamps are UnixNano — the daemon's native unit. Dividing by 1000 instead
// of 1e6 puts every job in 1970.
function when(ns?: number): string {
  if (!ns) return "";
  return new Date(ns / 1e6).toLocaleString();
}

function took(j: Job): string {
  if (!j.started_at || !j.finished_at) return "";
  const s = (j.finished_at - j.started_at) / 1e9;
  if (s < 90) return `${Math.round(s)}s`;
  const m = Math.floor(s / 60);
  return m < 90 ? `${m}m${Math.round(s % 60)}s` : `${Math.floor(m / 60)}h${m % 60}m`;
}
