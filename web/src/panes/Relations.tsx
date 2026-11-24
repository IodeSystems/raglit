import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { fetchDocRelations, type DocRelation, type DocSlice } from "../api";
import { useDoc } from "../useDocDetail";
import { Details } from "../ui/Details";
import { displayPath, fileName } from "../ui/display";

// Relations, similar and slices for a selected document, in one pane: the
// document-level view of how this document relates to the rest of the corpus.
//
// Relations are the rulings recorded in the judgements store: which document
// supersedes which, which is a correction of which, and what coverage was
// measured. Slices are named page ranges carved out of the document for
// citation. Neither is derivable from the index alone — both live in the
// audit trail — so this pane fetches them from /api/doc-relations.
//
// Similar documents come from the SAME /api/doc-detail the shell already
// fetched (detail.seen_in): near-duplicates with any ruling already made on
// the pair. The SeenIn tab shows that same data as its whole content; here it
// is one section of the superset view, so the reader sees all three kinds of
// connection without switching tabs.

export function Relations() {
  const { index, doc, detail } = useDoc();
  const [relations, setRelations] = useState<DocRelation[] | null>(null);
  const [slices, setSlices] = useState<DocSlice[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    setRelations(null);
    setSlices(null);
    setError("");
    fetchDocRelations(index, doc)
      .then((r) => {
        if (!live) return;
        setRelations(r.relations);
        setSlices(r.slices);
      })
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index, doc]);

  if (error) {
    return (
      <div className="empty">
        <div>{error}</div>
        <Details summary="Why might this be empty?">
          <div>
            Relations and slices are recorded in the audit trail. If the
            endpoint refused the request, the operator session may have
            expired, or the document is not in the index.
          </div>
        </Details>
      </div>
    );
  }

  if (!relations && !slices) {
    return <div className="empty">Loading…</div>;
  }

  const rels = relations ?? [];
  const sls = slices ?? [];

  return (
    <>
      <h3 className="section">Relations</h3>
      {rels.length === 0 ? (
        <div className="empty">
          No relations recorded for this document.
          <Details summary="What is a relation?">
            <div>
              A relation is a ruling about how this document relates to
              another: which one supersedes which, which is a correction,
              and what coverage was measured. Relations are recorded by the
              operator, not by the index.
            </div>
          </Details>
        </div>
      ) : (
        <table className="seen">
          <thead>
            <tr>
              <th>Kind</th>
              <th>Document</th>
              <th>Supersedes</th>
              <th>Coverage</th>
              <th>By</th>
              <th>When</th>
              <th>Note</th>
            </tr>
          </thead>
          <tbody>
            {rels.map((r, i) => (
              <tr key={i}>
                <td>{r.kind}</td>
                <td>
                  <Link to="/i/$index/d/$doc/pages" params={{ index, doc: r.b }}>
                    {fileName(r.b)}
                  </Link>
                  <div className="muted" style={{ fontSize: 11, wordBreak: "break-all" }}>
                    {displayPath(r.b)}
                  </div>
                </td>
                <td>{r.supersedes || <span className="muted">—</span>}</td>
                <td className="num">
                  {r.coverage != null && r.coverage > 0
                    ? `${Math.round(r.coverage * 100)}%`
                    : <span className="muted">—</span>}
                </td>
                <td>{r.by || <span className="muted">—</span>}</td>
                <td className="muted">{when(r.at)}</td>
                <td style={{ whiteSpace: "pre-wrap" }}>{r.note || ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3 className="section" style={{ marginTop: 20 }}>Similar</h3>
      {(detail.seen_in ?? []).length === 0 ? (
        <div className="empty">
          No other document in this index shares text with this one.
          <Details summary="Why might this be empty?">
            <div>
              Similar documents are found from a comparison index built
              separately. If it has not been built, this list is empty for
              every document.
            </div>
            <pre>raglit similar --build</pre>
          </Details>
        </div>
      ) : (
        <table className="seen">
          <thead>
            <tr>
              <th>Document</th>
              <th className="num">Shared text</th>
              <th>Relation</th>
              <th>Decision</th>
            </tr>
          </thead>
          <tbody>
            {(detail.seen_in ?? []).map((r) => (
              <tr key={r.path}>
                <td>
                  <Link to="/i/$index/d/$doc/pages" params={{ index, doc: r.path }}>
                    {r.title || fileName(r.path)}
                  </Link>
                  <div className="muted" style={{ fontSize: 11, wordBreak: "break-all" }}>
                    {displayPath(r.path, detail.root)}
                  </div>
                </td>
                <td className="num">
                  {r.jaccard != null ? `${Math.round(r.jaccard * 100)}%` : ""}
                </td>
                <td>{r.relation || <span className="muted">—</span>}</td>
                <td>{r.ruling || <span className="muted">Not decided</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3 className="section" style={{ marginTop: 20 }}>Slices</h3>
      {sls.length === 0 ? (
        <div className="empty">
          No slices carved from this document.
          <Details summary="What is a slice?">
            <div>
              A slice is a named page range carved out of the document for
              citation — "the signature is on pages 14–15" becomes a slice
              with a title and a note, addressable by its id.
            </div>
          </Details>
        </div>
      ) : (
        <table className="seen">
          <thead>
            <tr>
              <th>Id</th>
              <th>Pages</th>
              <th>Title</th>
              <th>By</th>
              <th>When</th>
              <th>Note</th>
            </tr>
          </thead>
          <tbody>
            {sls.map((s) => (
              <tr key={s.id}>
                <td className="muted" style={{ fontSize: 11 }}>{s.id}</td>
                <td className="num">{s.from}–{s.to}</td>
                <td>{s.title || <span className="muted">—</span>}</td>
                <td>{s.by || <span className="muted">—</span>}</td>
                <td className="muted">{when(s.at)}</td>
                <td style={{ whiteSpace: "pre-wrap" }}>{s.note || ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

const whenFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

function when(at?: string): string {
  if (!at) return "";
  const d = new Date(at);
  return Number.isNaN(d.getTime()) ? at : whenFmt.format(d);
}
