import { Link } from "@tanstack/react-router";

import { useDoc } from "../useDocDetail";
import { Details } from "../ui/Details";
import { displayPath, fileName } from "../ui/display";

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
        No other document in this index shares text with this one.
        <Details summary="Why might this be empty?">
          <div>
            Similar documents are found from a comparison index built separately.
            If it has not been built, this list is empty for every document.
          </div>
          <pre>raglit similar --build</pre>
        </Details>
      </div>
    );
  }
  return (
    <table className="seen">
      <thead>
        <tr>
          <th>Document</th>
          <th className="num" title="How much of the text the two documents share">Shared text</th>
          <th>Relation</th>
          <th>Decision</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.path}>
            <td>
              <Link to="/i/$index/d/$doc/pages" params={{ index, doc: r.path }}>
                {r.title || fileName(r.path)}
              </Link>
              <div className="muted" style={{ fontSize: 11, wordBreak: "break-all" }}>
                {displayPath(r.path, detail.root)}
              </div>
            </td>
            <td className="num">{r.jaccard != null ? `${Math.round(r.jaccard * 100)}%` : ""}</td>
            <td>{r.relation || <span className="muted">—</span>}</td>
            <td>{r.ruling || <span className="muted">Not decided</span>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
