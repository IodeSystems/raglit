import { Link } from "@tanstack/react-router";

import { listProjects, type Project } from "../api";
import { usePoll } from "../usePoll";

// The whole daemon, at the top.
//
// "/" used to redirect to whichever index happened to be first, which made the
// daemon look like a single-corpus tool and buried the fact that it serves
// several projects at once. It also cost the legacy-hash rewrite a race it could
// not win: the redirect fired in beforeLoad, so an old link was resolved to the
// first index before anything mounted. There is no redirect now, so that is gone
// as well.
//
// Projects come first because that is the unit people actually pick — an index
// name like `delano-v-mckinnon__default` is a project and an index run together,
// and reading nine of those was the complaint this answers.
export function Overview() {
  const { data, error } = usePoll<{ projects?: Project[] }>(listProjects, 5000, []);

  const projects = data?.projects ?? [];

  const total = (pick: (p: Project) => number | undefined) =>
    projects.reduce((n, p) => n + (pick(p) ?? 0), 0);

  const cards: [string, number][] = [
    ["projects", projects.filter((p) => p.name).length],
    ["indexes", total((p) => p.indexes?.length)],
    ["documents", total((p) => p.documents)],
    ["fragments", total((p) => p.fragments)],
    ["pending", total((p) => p.pending)],
    ["running", total((p) => p.running)],
    ["failed", total((p) => p.failed)],
  ];

  // Its own header and main, because RootShell is deliberately a bare Outlet:
  // the index picker and the search box below it need a scope this route does
  // not have. Without this the overview rendered with no chrome at all.
  return (
    <>
      <header className="pageheader">
        <h1>
          raglit <span>review</span>
        </h1>
        <nav className="crumbs">
          <strong>projects</strong>
        </nav>
        <div className="grow" />
      </header>

      <main className="pagemain">
        {error && <div className="empty err">{error}</div>}
        {!data && !error && <div className="empty">loading…</div>}
        {data && !projects.length && (
          <div className="empty">This daemon has no indexes yet.</div>
        )}
        {!!projects.length && (
          <>
            <div className="cards">
              {cards.map(([k, n]) => (
                <div className="card" key={k}>
                  <div className="n">{n}</div>
                  <div className="k">{k}</div>
                </div>
              ))}
            </div>

            <h2>Projects</h2>
            {projects.map((p) => (
              <ProjectCard key={p.name || "(unnamespaced)"} p={p} />
            ))}
          </>
        )}
      </main>
    </>
  );
}

function ProjectCard({ p }: { p: Project }) {
  // A project with no namespace is not a project. It is whatever was indexed
  // without one — `default` on this daemon — and calling it a project would
  // invent a thing that does not exist, so it is named for what it is and has no
  // project route to click into.
  const unnamespaced = !p.name;
  const indexes = p.indexes ?? [];
  const roots = indexes.filter((i) => !i.parent);
  const branches = indexes.filter((i) => i.parent);

  return (
    <div className="panel projcard">
      <div className="projhead">
        <h3>
          {unnamespaced ? (
            <span className="muted">indexes with no project namespace</span>
          ) : (
            <Link to="/p/$project" params={{ project: p.name }}>
              {p.name}
            </Link>
          )}
        </h3>
        <div className="grow" />
        <span className="muted">
          {[
            `${roots.length} index${roots.length === 1 ? "" : "es"}`,
            branches.length ? `${branches.length} branch${branches.length === 1 ? "" : "es"}` : null,
            `${p.documents ?? 0} docs`,
            p.pending ? `${p.pending} pending` : null,
            p.running ? `${p.running} running` : null,
            p.failed ? `${p.failed} failed` : null,
          ]
            .filter(Boolean)
            .join(" · ")}
        </span>
      </div>
      {/* Absent is not "not watching": a project the daemon has never been told
          the home of cannot be watched and has not declined to be. Only say
          something when there is something to say. */}
      {p.home && (
        <div className="muted small">
          {p.watching ? "watching " : "registered (watch off) "}
          {p.home}
          {p.files ? ` · ${p.files} source file(s)` : ""}
        </div>
      )}
      <div className="idxlist">
        {indexes.map((i) => (
          <Link
            className="idxchip"
            key={i.name}
            to="/i/$index"
            params={{ index: i.name }}
            title={i.name}
          >
            {i.parent && <span className="branchmark">⑂</span>}
            {i.local}
            <span className="muted"> {i.documents ?? 0}</span>
            {!!i.failed && <span className="badge error">{i.failed}</span>}
          </Link>
        ))}
      </div>
    </div>
  );
}
