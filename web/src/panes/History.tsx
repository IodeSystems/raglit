import { useDoc } from "../useDocDetail";

// What has happened to this document: readings taken, and verdicts ruled.
//
// Ordered as the server returned it. These rows accumulate and are never
// rewritten — a later correction does not replace an earlier reading, it is
// another fact about the same page — so the list IS the record, not a view of a
// current state.
export function History() {
  const { detail } = useDoc();
  const events = detail.history ?? [];
  if (!events.length) {
    return <div className="empty">Nothing recorded for this document yet.</div>;
  }
  return (
    <table className="seen">
      <thead>
        <tr>
          <th>What</th>
          <th className="num">Page</th>
          <th>Source</th>
          <th>By</th>
          <th>When</th>
          <th>Note</th>
        </tr>
      </thead>
      <tbody>
        {events.map((e, i) => (
          <tr key={`${e.kind}-${e.seq ?? i}-${e.page ?? ""}-${i}`}>
            <td>
              {e.kind}
              {/* "active" is which reading is the one in the index right now.
                  Without it a page with three readings gives no clue which one
                  search is actually matching against. */}
              {e.active && <span className="badge" style={{ marginLeft: 6 }}>active</span>}
            </td>
            <td className="num">{e.page || ""}</td>
            <td>{e.source || ""}</td>
            <td>{e.by || <span className="muted">—</span>}</td>
            <td className="muted">{e.at || ""}</td>
            <td style={{ whiteSpace: "pre-wrap" }}>{e.note || ""}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
