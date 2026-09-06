import { Link, useParams } from "@tanstack/react-router";

import { listProjects, type Project as ProjectRow, type ProjectIndex } from "../api";
import { usePoll } from "../usePoll";

// One project: the indexes it owns, with its branches under the index each
// overlays rather than beside it.
//
// A branch IS an index — it is in reg.Names(), it answers every endpoint, and
// nothing in its name says what it is. Listed flat, `dun-main` reads as a
// sibling of `dun` when it may be a copy-on-write view of it, and the two
// document counts are not comparable: a branch stores only its DIFFS, so its
// count is what it changed, not what it holds.
export function Project() {
  const { project } = useParams({ from: "/p/$project" });
  const { data, error } = usePoll<{ projects?: ProjectRow[] }>(listProjects, 5000, []);

  if (error) return <div className="empty err">{error}</div>;
  if (!data) return <div className="empty">loading…</div>;
  const p = (data.projects ?? []).find((x) => x.name === project);
  if (!p) {
    return (
      <div className="empty">
        No project “{project}” on this daemon. A project exists because an index
        carries its prefix; nothing creates one on its own.
      </div>
    );
  }

  const indexes = p.indexes ?? [];
  const roots = indexes.filter((i) => !i.parent);
  const orphans = indexes.filter((i) => i.parent && !indexes.some((r) => r.name === i.parent));

  const cards: [string, number | string][] = [
    ["indexes", roots.length],
    ["documents", p.documents ?? 0],
    ["fragments", p.fragments ?? 0],
    ["pending", p.pending ?? 0],
    ["running", p.running ?? 0],
    ["failed", p.failed ?? 0],
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

      {p.home && (
        <p className="why">
          {p.watching ? "Watched for changes: " : "Registered, watch off: "}
          <code>{p.home}</code>
          {p.files ? ` — ${p.files} source file(s) planned` : ""}
        </p>
      )}

      <h2>Indexes</h2>
      <div className="panel">
        {roots.map((i) => (
          <IndexRow key={i.name} i={i} branches={indexes.filter((b) => b.parent === i.name)} />
        ))}
        {/* A branch whose parent is not in this project still has to appear.
            Silently dropping it would hide a whole corpus because its lineage
            crosses a namespace. */}
        {orphans.map((i) => (
          <IndexRow key={i.name} i={i} branches={[]} orphan />
        ))}
      </div>
    </>
  );
}

function IndexRow({
  i,
  branches,
  orphan,
}: {
  i: ProjectIndex;
  branches: ProjectIndex[];
  orphan?: boolean;
}) {
  return (
    <>
      <div className="doc-row">
        <div className="doc-title">
          <Link to="/i/$index" params={{ index: i.name }}>
            {i.local}
          </Link>
          <small>{i.name}</small>
        </div>
        <span className="muted">{i.documents ?? 0} docs</span>
        <span className="muted">{i.fragments ?? 0} frags</span>
        {!!i.pending && <span className="badge pending">{i.pending} pending</span>}
        {!!i.running && <span className="badge running">{i.running} running</span>}
        {!!i.failed && (
          <Link className="badge error" to="/i/$index/health" params={{ index: i.name }}>
            {i.failed} failed
          </Link>
        )}
        {orphan && i.parent && (
          <span className="badge" title={`branch of ${i.parent}, which is not in this project`}>
            ⑂ {i.parent}
          </span>
        )}
      </div>
      {branches.map((b) => (
        <div className="doc-row branchrow" key={b.name}>
          <div className="doc-title">
            <span className="branchmark">⑂</span>
            <Link to="/i/$index" params={{ index: b.name }}>
              {b.local}
            </Link>
            <small>branch of {i.local}</small>
          </div>
          {/* A branch's count is its OWN rows — what it changed — not what it
              can see. Labelled, because "3 docs" beside a parent's "481" reads
              as a nearly empty index rather than three edits. */}
          <span className="muted">{b.documents ?? 0} local changes</span>
        </div>
      ))}
    </>
  );
}
