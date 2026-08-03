import { Link } from "@tanstack/react-router";

import { useDoc } from "../useDocDetail";

// Where else this content shows up: near-duplicates, and any ruling already made
// on the pair.
//
// The ruling column matters more than the score. A 0.92 overlap with no ruling
// is a question; the same 0.92 with "copy" recorded is answered, and showing
// them identically sends somebody to re-decide work that is done.
export function SeenIn() {
  const { detail, index } = useDoc();
  const rows = detail.seen_in ?? [];
  if (!rows.length) {
    return (
      <div className="empty">
        Nothing else in this index shares content with it — or sketches have not
        been built (<code>raglit similar --build</code>).
      </div>
    );
  }
  return (
    <table className="seen">
      <thead>
        <tr>
          <th>Document</th>
          <th className="num">Overlap</th>
          <th>Relation</th>
          <th>Ruling</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.path}>
            <td>
              <Link to="/i/$index/d/$doc/pages" params={{ index, doc: r.path }}>
                {r.title || r.path.split("/").pop()}
              </Link>
              <div className="muted" style={{ fontSize: 11, wordBreak: "break-all" }}>
                {r.path}
              </div>
            </td>
            <td className="num">{r.jaccard != null ? r.jaccard.toFixed(2) : ""}</td>
            <td>{r.relation || <span className="muted">—</span>}</td>
            <td>{r.ruling || <span className="muted">unruled</span>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
