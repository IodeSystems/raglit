import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { search, type Hit } from "../api";

// Search results for a query that lives in the URL.
//
// The query is a search param rather than component state, so a search is a link
// and the back button walks searches. Somebody finding the right query for a
// corpus can now send it to somebody else, which is the ordinary thing people
// wanted to do and could not.
export function Search() {
  const { index } = useParams({ from: "/i/$index" });
  const { q, mode } = useSearch({ from: "/i/$index/search" });
  const navigate = useNavigate();
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!q) {
      setHits(null);
      return;
    }
    let live = true;
    setError("");
    setHits(null);
    search(index, q, mode)
      .then((r) => live && setHits(r.hits ?? []))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index, q, mode]);

  return (
    <div className="panel">
      <div className="searchbar">
        <span className="muted">{q ? <>results for “{q}”</> : "Nothing searched yet."}</span>
        <div className="grow" />
        <select
          value={mode}
          onChange={(e) =>
            navigate({
              to: "/i/$index/search",
              params: { index },
              search: { q, mode: e.target.value },
            })
          }
        >
          <option value="bm25">bm25 — exact words</option>
          <option value="hybrid">hybrid — words + meaning</option>
          <option value="vec">vector — meaning only</option>
        </select>
      </div>

      {error && <div className="empty err">{error}</div>}
      {!error && q && hits === null && <div className="empty">searching…</div>}
      {hits?.length === 0 && <div className="empty">No hits.</div>}
      {hits?.map((h, i) => (
        <div className="hit" key={`${h.doc_id}-${h.page ?? 0}-${i}`}>
          {/* Into the transcript, not the pages: a hit is a piece of TEXT, and
              landing on a page image makes the reader find the words again. */}
          <Link to="/i/$index/d/$doc/transcript" params={{ index, doc: h.doc_id }}>
            {h.title || h.doc_id.split("/").pop()}
          </Link>
          {/* Rendered as TEXT. A snippet is words out of the corpus, and a
              corpus can contain markup — the page this replaces said so and
              used textContent for exactly this reason. */}
          {h.snippet && <div className="snip">{h.snippet}</div>}
          <div className="meta">
            {[
              h.page ? `page ${h.page}` : null,
              `score ${(h.score ?? 0).toFixed(2)}`,
              h.doc_id,
            ]
              .filter(Boolean)
              .join(" · ")}
          </div>
        </div>
      ))}
    </div>
  );
}
