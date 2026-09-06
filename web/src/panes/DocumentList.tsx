import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";

import { listDocuments, type DocumentRow } from "../api";

// Every document in the index.
//
// The filter is local state, not a URL param, and that is deliberate: it is a
// way to find a row on this screen, not a thing worth sending anybody. The
// search route is what you send.
export function DocumentList() {
  const { index } = useParams({ from: "/i/$index" });
  const [docs, setDocs] = useState<DocumentRow[] | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState("");

  useEffect(() => {
    let live = true;
    setDocs(null);
    setError("");
    listDocuments(index)
      .then((r) => live && setDocs(r.documents ?? []))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index]);

  const shown = useMemo(() => {
    if (!docs) return null;
    const f = filter.trim().toLowerCase();
    if (!f) return docs;
    return docs.filter(
      (d) => d.path.toLowerCase().includes(f) || (d.name ?? "").toLowerCase().includes(f),
    );
  }, [docs, filter]);

  return (
    <div className="panel">
      <div className="searchbar">
        <input
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="filter by name or path"
          autoComplete="off"
        />
      </div>
      {error && <div className="empty err">{error}</div>}
      {!error && shown === null && <div className="empty">loading…</div>}
      {shown?.length === 0 && <div className="empty">No documents match.</div>}
      {shown?.map((d) => (
        <Link
          className="doc-row"
          key={d.path}
          to="/i/$index/d/$doc/pages"
          params={{ index, doc: d.path }}
        >
          <div className="doc-title">
            {/* What a person or a machine said it IS, above what the file is
                called. The path is still shown underneath, because the title is
                the thing most likely to be wrong. */}
            {d.name || d.path.split("/").pop()}
            <small>{d.path}</small>
          </div>
          {d.kind && <span className="badge">{d.kind}</span>}
          {!!d.vision && <span className="badge vision">vision {d.vision}</span>}
          <span className="muted">{d.fragments ?? 0} frags</span>
        </Link>
      ))}
    </div>
  );
}
