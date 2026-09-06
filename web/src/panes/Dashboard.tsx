import { useParams } from "@tanstack/react-router";

import { getStatus, listChannels, type LaneStatus, type ModelChannel, type StatusSnapshot } from "../api";
import { usePoll } from "../usePoll";

// Index size and queue state, refreshed while you watch it.
//
// Polled rather than pushed, same as the page this replaces. An ingest run is
// the thing people actually sit in front of this for, and a static snapshot of a
// queue that is draining tells them nothing.
export function Dashboard() {
  const { index } = useParams({ from: "/i/$index" });
  const { data, error } = usePoll<StatusSnapshot>(() => getStatus(index), 3000, [index]);

  if (error) return <div className="empty err">{error}</div>;
  if (!data) return <div className="empty">loading…</div>;

  const cards: [string, number | string][] = [
    ["documents", data.documents ?? 0],
    ["fragments", data.fragments ?? 0],
    ["pending", data.pending ?? 0],
    ["running", data.running ?? 0],
    ["done", data.done ?? 0],
    ["failed", data.failed ?? 0],
    ["jobs/min", data.jobs_per_min != null ? data.jobs_per_min.toFixed(1) : "—"],
  ];

  return (
    <>
      <div className="cards">
        {cards.map(([k, n]) => (
          <div className="card" key={k}>
            <div className="n">{n}</div>
            <div className="k">{k}</div>
          </div>
        ))}
      </div>
      <Lanes lanes={data.lanes} />
      <Channels />
    </>
  );
}

// The queue by lane, and the models it is waiting on.
//
// Two different questions, and they used to be conflated into one number. The
// LANE says what kind of work is queued — "12 pending" is an afternoon if they
// are scans and under a minute if they are text files. The CHANNEL says what is
// actually limiting throughput, which is never the lane: it is how many calls
// each model will take at once.
function Lanes({ lanes }: { lanes?: Record<string, LaneStatus> }) {
  const rows = Object.entries(lanes ?? {}).filter(
    ([, l]) => (l.pending ?? 0) + (l.running ?? 0) > 0,
  );
  if (!rows.length) return null;
  rows.sort(([a], [b]) => (a === "heavy" ? -1 : b === "heavy" ? 1 : a.localeCompare(b)));

  return (
    <>
      <h2>Queued work</h2>
      <div className="panel probgroup">
        {rows.map(([name, l]) => (
          <div className="prob" key={name}>
            <div className="subj">
              <span className={"badge lane-" + name}>{name}</span>{" "}
              <span className="muted">
                {l.running ?? 0} running
                {l.pending ? ` · ${l.pending} waiting` : ""}
              </span>
            </div>
          </div>
        ))}
      </div>
    </>
  );
}

// Per-model admission, which is what actually bounds ingest.
//
// There used to be one number for the whole endpoint — vision ran a job at a
// time because "the GPU admits one" — and it serialised models that live on
// different cards. Each model has its own channel now, and the width is learned
// from the server's own backpressure rather than configured, so a changing model
// layout needs no edit here.
function Channels() {
  const { data } = usePoll<{ channels?: ModelChannel[] }>(listChannels, 3000, []);
  const chans = data?.channels ?? [];
  if (!chans.length) return null;

  return (
    <>
      <h2>Model channels</h2>
      <div className="panel probgroup">
        <p className="why">
          Each model is admitted on its own; nothing coordinates across them, so
          models on different cards run at the same time. The width starts at one
          and grows only while calls succeed — a 429 halves it, because the
          server’s capacity is shared with other traffic.
        </p>
        <table>
          <thead>
            <tr>
              <th>Model</th>
              <th className="num">Width</th>
              <th className="num">In flight</th>
              <th className="num">Peak</th>
              <th className="num">Calls</th>
              <th className="num">429s</th>
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
                <td className="num">{c.calls ?? 0}</td>
                <td className={"num " + (c.n_429 ? "err" : "muted")}>{c.n_429 ?? 0}</td>
                <td>
                  {/* Chilled is not an error: the model is being polite to
                      whoever else is using it. Saying so stops it reading as a
                      stall. */}
                  {c.chilled && (
                    <span className="badge warn" title="held narrow after backpressure; it will widen again">
                      backing off
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
