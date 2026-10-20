import { Link } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";

import { listIndexes, type IndexInfo, type ProjectIndex } from "../api";
import { documents, passages } from "../ui/display";
import "./scope.css";

// The indexes of one project, as the overview and the project page both show
// them: one row per index with what it holds, its branches under it.
//
// A branch IS an index — it answers every endpoint, and nothing in its name
// says what it is. Listed flat, `dun-main` reads as a sibling of `dun` when it
// may be a copy-on-write view of it, and the two document counts are not
// comparable: a branch stores only its DIFFS, so its count is what it changed.
export function IndexRows({
  indexes,
  info,
}: {
  indexes: ProjectIndex[];
  info: Map<string, IndexInfo>;
}) {
  const roots = indexes.filter((i) => !i.parent);
  // A branch whose parent is not in this project still has to appear. Dropping
  // it would hide a whole corpus because its lineage crosses a namespace.
  const orphans = indexes.filter((i) => i.parent && !indexes.some((r) => r.name === i.parent));

  return (
    <div className="idxrows">
      {roots.map((i) => (
        <IndexRow
          key={i.name}
          i={i}
          info={info.get(i.name)}
          branches={indexes.filter((b) => b.parent === i.name)}
        />
      ))}
      {orphans.map((i) => (
        <IndexRow key={i.name} i={i} info={info.get(i.name)} branches={[]} orphan />
      ))}
    </div>
  );
}

function IndexRow({
  i,
  info,
  branches,
  orphan,
}: {
  i: ProjectIndex;
  info?: IndexInfo;
  branches: ProjectIndex[];
  orphan?: boolean;
}) {
  const kinds = (info?.kinds ?? []).filter((k) => k.tag !== "other").slice(0, 4);
  return (
    <>
      <div className="idxrow">
        <div className="idxrow-main">
          <Link className="idxrow-name" to="/i/$index" params={{ index: i.name }}>
            {orphan && i.parent ? "⑂ " : ""}
            {i.local}
          </Link>
          <div className="idxrow-facts">
            {documents(i.documents ?? 0)} · {passages(i.fragments ?? 0)}
            {orphan && i.parent && <> · branch of {i.parent}</>}
          </div>
          {!!kinds.length && (
            <div className="idxrow-kinds">
              {kinds.map((k) => (
                <span className="kindchip" key={k.tag}>
                  {k.tag} <span className="muted">{k.count.toLocaleString()}</span>
                </span>
              ))}
            </div>
          )}
        </div>
        <IndexState i={i} />
      </div>
      {branches.map((b) => (
        <div className="idxrow branch" key={b.name}>
          <div className="idxrow-main">
            <Link className="idxrow-name" to="/i/$index" params={{ index: b.name }}>
              ⑂ {b.local}
            </Link>
            {/* A branch's count is its OWN rows — what it changed — not what
                it can see. Labelled, because "3 documents" beside a parent's
                "481" reads as a nearly empty index rather than three edits. */}
            <div className="idxrow-facts">
              Branch of {i.local} · {(b.documents ?? 0).toLocaleString()} changed{" "}
              {b.documents === 1 ? "document" : "documents"}
            </div>
          </div>
          <IndexState i={b} />
        </div>
      ))}
    </>
  );
}

// Busy and broken, in words. Nothing at all when the index is idle and clean:
// an "OK" badge on every row is noise a reader learns to skip.
function IndexState({ i }: { i: ProjectIndex }) {
  const busy = (i.pending ?? 0) + (i.running ?? 0);
  return (
    <div className="idxrow-state">
      {busy > 0 && (
        <span className="badge running">
          {busy.toLocaleString()} importing
        </span>
      )}
      {!!i.failed && (
        <Link className="badge error" to="/i/$index/health" params={{ index: i.name }}>
          {i.failed.toLocaleString()} failed {i.failed === 1 ? "import" : "imports"}
        </Link>
      )}
    </div>
  );
}

// /indexes carries each index's digest — the kinds with counts — which
// /api/projects does not. Read once; kinds change on import, not per second.
export function useIndexInfo(): Map<string, IndexInfo> {
  const [rows, setRows] = useState<IndexInfo[]>([]);
  useEffect(() => {
    let live = true;
    listIndexes()
      .then((got) => live && setRows(got.indexes ?? []))
      .catch(() => live && setRows([]));
    return () => {
      live = false;
    };
  }, []);
  return useMemo(() => new Map(rows.map((r) => [r.name, r])), [rows]);
}
