import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";

import { getJob, listJobs, postJSON, type Job, type JobStage } from "../api";
import { usePoll } from "../usePoll";
import { Details, ErrorText } from "../ui/Details";
import { fileName, humanizeError, laneLabel, passages } from "../ui/display";
import {
  docParam,
  docRel,
  engineText,
  isRetryTally,
  retryEach,
  retryJob,
  retryText,
  segModeLabel,
  stageLabel,
  stateLabel,
  useIndexRoot,
} from "./opsText";
import "./ops.css";

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
  const { root, ready } = useIndexRoot(index);
  const { data, error, refresh } = usePoll(() => listJobs(index), 3000, [index]);
  const [busy, setBusy] = useState<number | null>(null);
  const [actErr, setActErr] = useState("");
  const [bulk, setBulk] = useState(false);
  const [note, setNote] = useState("");

  const act = async (kind: "retry" | "cancel" | "forget", id: number) => {
    setBusy(id);
    setActErr("");
    try {
      await postJSON(`/api/jobs/${kind}`, { index }, { id });
      refresh();
    } catch (e: unknown) {
      setActErr(String(e));
    } finally {
      setBusy(null);
    }
  };

  if (error)
    return (
      <div className="empty ops">
        The import queue could not be loaded.
        <ErrorText raw={error} />
      </div>
    );
  if (!data || !ready) return <div className="empty">Loading…</div>;
  const jobs = data.jobs ?? [];
  if (!jobs.length) return <div className="empty">No imports yet.</div>;

  const failed = jobs.filter((j) => j.state === "error");
  const retryFailed = async () => {
    setBulk(true);
    setNote("");
    const n = await retryEach(failed, (j) => retryJob(index, j.id));
    setNote(n ? `${n.toLocaleString()} could not be retried.` : "Retrying.");
    setBulk(false);
    refresh();
  };

  return (
    <div className="ops">
      {failed.length > 0 && (
        <div className="ops-head">
          <span className="ops-note">
            {failed.length.toLocaleString()} of the {jobs.length.toLocaleString()} imports shown did not finish.
          </span>
          <button className="act primary" disabled={bulk} onClick={retryFailed}>
            {bulk ? "Retrying…" : `Retry ${failed.length.toLocaleString()} failed`}
          </button>
          {note && <span className="ops-note">{note}</span>}
        </div>
      )}
      {actErr && <ErrorText raw={actErr} />}
      <div className="panel table-scroll">
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>Status</th>
              <th>Size</th>
              <th>Method</th>
              <th>Document</th>
              <th className="num">Passages</th>
              <th>Time</th>
              <th>Detail</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {jobs.map((j) => {
              const rel = docRel(j.url, root);
              return (
                <tr key={j.id} data-job={j.id}>
                  <td>
                    <Link to="/i/$index/jobs/$jobId" params={{ index, jobId: String(j.id) }}>
                      {j.id}
                    </Link>
                  </td>
                  <td>
                    <span className={"badge " + j.state}>{stateLabel(j.state)}</span>
                  </td>
                  <td>{j.lane && <span className={"badge lane-" + j.lane}>{laneLabel(j.lane)}</span>}</td>
                  <td>{segModeLabel(j.mode)}</td>
                  <td className="target" title={rel}>
                    <Link className="doc-name" to="/i/$index/d/$doc/pages" params={{ index, doc: docParam(j.url, root) }}>
                      {fileName(rel)}
                    </Link>
                    {fileName(rel) !== rel && <div className="doc-path">{rel}</div>}
                  </td>
                  <td className="num">{j.fragments ? j.fragments.toLocaleString() : ""}</td>
                  <td>{j.state === "pending" || j.state === "running" ? eta(j.eta_seconds) : took(j)}</td>
                  <td className="detail">
                    <JobDetailCell job={j} />
                  </td>
                  <td className="actions">
                    <JobActions job={j} busy={busy === j.id} act={act} />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// The list's one-line account of a job: its error, else the stage that warned,
// else the last thing it did.
function JobDetailCell({ job }: { job: Job }) {
  if (job.error) return <ErrorText raw={job.error} />;
  const s = lastStage(job);
  if (!s?.detail) return null;
  return <StageDetail stage={s} />;
}

// A stage's detail as a person reads it. Errors are humanized and retry tallies
// put into words; the stage's verbatim text — the endpoint's own words, which
// name the block a repetition guard looped on — stays one click away.
function StageDetail({ stage }: { stage: JobStage }) {
  const d = stage.detail ?? "";
  if (!d) return null;
  if (stage.name === "llm-retries" || isRetryTally(d)) {
    return (
      <div className="errline">
        <div className="stage-detail">{retryText(d)}</div>
        <Details>
          <pre>{d}</pre>
        </Details>
      </div>
    );
  }
  if (stage.state === "error" || stage.state === "degraded") {
    const h = humanizeError(d);
    return (
      <div className="errline">
        <div className="errline-summary">{h.summary}</div>
        <Details>
          <pre>{h.detail}</pre>
        </Details>
      </div>
    );
  }
  return <div className="stage-detail">{d}</div>;
}

// One job, fetched BY ID so its age does not matter, with every stage the
// pipeline recorded. The daemon has always returned these; nothing rendered
// them, which is why a health row could name a bad ingest and lead nowhere.
function JobDetail({ index, id }: { index: string; id: number }) {
  const { root, ready } = useIndexRoot(index);
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

  if (error)
    return (
      <div className="empty ops">
        This import could not be loaded.
        <ErrorText raw={error} />
      </div>
    );
  if (!data || !ready) return <div className="empty">Loading…</div>;
  const j = data;
  const stages = j.stages ?? [];
  const rel = docRel(j.url, root);

  return (
    <div className="ops">
      <div className="panel jobhead">
        <div className="subj">
          <span className="badge">Import #{j.id}</span>{" "}
          <span className={"badge " + j.state}>{stateLabel(j.state)}</span>{" "}
          {j.lane && <span className={"badge lane-" + j.lane}>{laneLabel(j.lane)}</span>}{" "}
          {j.mode && <span className="badge">{segModeLabel(j.mode)}</span>}{" "}
          <Link className="doc-name" to="/i/$index/d/$doc/pages" params={{ index, doc: docParam(j.url, root) }}>
            {fileName(rel)}
          </Link>
        </div>
        {fileName(rel) !== rel && <div className="doc-path">{rel}</div>}
        <div className="muted">
          {j.fragments ? `${passages(j.fragments)} · ` : ""}
          queued {when(j.enqueued_at)}
          {j.started_at ? ` · started ${when(j.started_at)}` : ""}
          {j.finished_at ? ` · took ${took(j)}` : ""}
        </div>
        {j.error && <ErrorText raw={j.error} />}
        <div className="acts">
          <JobActions job={j} busy={busy} act={(k) => act(k)} />
          <Link className="act" to="/i/$index/jobs" params={{ index }}>
            All imports
          </Link>
        </div>
        {actErr && <ErrorText raw={actErr} />}
      </div>

      {!stages.length ? (
        <div className="panel">
          <h3>Steps</h3>
          <p className="why">
            No steps were recorded. The import was queued before step logging existed, or it never
            started.
          </p>
        </div>
      ) : (
        runsOf(stages).map((run, i, all) => (
          <div className="panel probgroup" key={run[0]!.id}>
            <h3>
              {all.length > 1 ? `Attempt ${i + 1} of ${all.length}` : "Steps"}
              {run[0]?.at ? <span className="muted"> · {when(run[0].at)}</span> : null}
            </h3>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>#</th>
                    <th>Step</th>
                    <th>Method</th>
                    <th>Status</th>
                    <th>Detail</th>
                  </tr>
                </thead>
                <tbody>
                  {run.map((s) => (
                    <tr key={s.id}>
                      <td>{s.seq}</td>
                      <td>{stageLabel(s.name)}</td>
                      <td>{s.name === "lane" ? laneLabel(s.engine) : engineText(s.engine)}</td>
                      <td>
                        <span className={"badge " + stageClass(s.state)}>{stateLabel(s.state)}</span>
                      </td>
                      <td className="detail">
                        <StageDetail stage={s} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ))
      )}
    </div>
  );
}

// Split a job's stages into its RUNS.
//
// A retry appends a whole second pipeline to the same job, with seq restarting
// at 1 — so a job retried four times carries four fetch stages, four extracts,
// and four different segment outcomes under one id. Job 927 on the fence-dispute index
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
// job that COMPLETED after retries, which is a warning that a failure is
// coming, not a failure.
function stageClass(state: string): string {
  return state === "error" ? "error" : state === "warn" ? "warn" : state;
}

function lastStage(j: Job): JobStage | undefined {
  const s = j.stages;
  if (!s?.length) return undefined;
  const bad = s.filter((x) => x.state === "error" || x.state === "warn").pop();
  return bad ?? s[s.length - 1]!;
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
  const failed = job.state === "error";
  return (
    <>
      {terminal && (
        <button
          className={"act" + (failed ? " primary" : "")}
          disabled={busy}
          onClick={() => act("retry", job.id)}
        >
          {failed ? "Retry" : "Run again"}
        </button>
      )}
      {job.state === "pending" && (
        <button className="act" disabled={busy} onClick={() => act("cancel", job.id)}>
          Cancel
        </button>
      )}
      {terminal && (
        <button
          className="act"
          disabled={busy}
          title="Remove this record from the list. The document is not changed."
          onClick={() => act("forget", job.id)}
        >
          Dismiss
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
  if (s < 90) return `${Math.round(s)} s`;
  const m = Math.floor(s / 60);
  return m < 90 ? `${m} min ${Math.round(s % 60)} s` : `${Math.floor(m / 60)} h ${m % 60} min`;
}
