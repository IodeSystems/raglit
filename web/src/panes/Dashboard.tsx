import { useParams } from "@tanstack/react-router";

import { getStatus, type StatusSnapshot } from "../api";
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
    <div className="cards">
      {cards.map(([k, n]) => (
        <div className="card" key={k}>
          <div className="n">{n}</div>
          <div className="k">{k}</div>
        </div>
      ))}
    </div>
  );
}
