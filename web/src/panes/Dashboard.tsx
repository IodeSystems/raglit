import { useParams } from "@tanstack/react-router";

import { getStatus, type LaneStatus, type StatusSnapshot } from "../api";
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
    </>
  );
}

// The queue by lane.
//
// "7 pending" is not an answer: seven scans is an afternoon of vision and seven
// markdown files is under a minute, and until the queue was split by lane there
// was no way to tell which without reading every URL. Shown with each lane's
// slot count, because that is what turns a depth into a wait — one slot means
// the seventh job starts after six others finish.
function Lanes({ lanes }: { lanes?: Record<string, LaneStatus> }) {
  const rows = Object.entries(lanes ?? {});
  if (!rows.length) return null;
  // heavy first: it is the one that makes people wait.
  rows.sort(([a], [b]) => (a === "heavy" ? -1 : b === "heavy" ? 1 : a.localeCompare(b)));
  const busy = rows.some(([, l]) => (l.pending ?? 0) + (l.running ?? 0) > 0);

  return (
    <>
      <h2>Ingest lanes</h2>
      <div className="panel probgroup">
        <p className="why">
          Vision runs one job at a time — the model admits one — and everything
          else runs several at once, so a slow scan no longer holds up a text
          file behind it.
        </p>
        {!busy && <p className="why">Both lanes are idle.</p>}
        {rows.map(([name, l]) => (
          <div className="prob" key={name}>
            <div className="subj">
              <span className={"badge lane-" + name}>{name}</span>{" "}
              <span className="muted">
                {l.running ?? 0} running of {l.slots ?? 1} slot
                {(l.slots ?? 1) === 1 ? "" : "s"}
                {l.pending ? ` · ${l.pending} waiting` : " · nothing waiting"}
              </span>
            </div>
          </div>
        ))}
      </div>
    </>
  );
}
