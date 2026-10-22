import { Link } from "@tanstack/react-router";

import { listProjects, type IndexInfo, type Project } from "../api";
import { usePoll } from "../usePoll";
import { ErrorText } from "../ui/Details";
import { displayPath, documents, passages } from "../ui/display";
import { AppShell } from "../shell/AppShell";
import { settingsSection, type NavSection } from "../shell/NavDrawer";
import { IndexRows, useIndexInfo } from "./IndexRows";
import "./scope.css";

// The whole daemon, at the top.
//
// "/" used to redirect to whichever index happened to be first, which made the
// daemon look like a single-corpus tool and buried the fact that it serves
// several projects at once. It also cost the legacy-hash rewrite a race it could
// not win: the redirect fired in beforeLoad, so an old link was resolved to the
// first index before anything mounted. There is no redirect now.
//
// Projects come first because that is the unit people actually pick — an index
// name like `bramble-v-quill__default` is a project and an index run together.
// Each index row says what it holds (the kinds from the digest), not only how
// big it is: seven stat cards told a reader nothing about which corpus is which.
export function Overview() {
  const { data, error } = usePoll<{ projects?: Project[] }>(listProjects, 5000, []);
  const info = useIndexInfo();
  const projects = data?.projects ?? [];

  const named = projects.filter((p) => p.name);
  const indexCount = projects.reduce((n, p) => n + (p.indexes?.length ?? 0), 0);
  const docCount = projects.reduce((n, p) => n + (p.documents ?? 0), 0);

  return (
    <AppShell crumbs={["Projects"]} nav={rootSections(projects)}>
      <div className="scopehead">
        <h1>Projects</h1>
        {!!projects.length && (
          <div className="facts">
            {named.length.toLocaleString()} {named.length === 1 ? "project" : "projects"}
            <span className="sep">·</span>
            {indexCount.toLocaleString()} {indexCount === 1 ? "index" : "indexes"}
            <span className="sep">·</span>
            {documents(docCount)}
          </div>
        )}
      </div>

      {error && (
        <div className="empty">
          <ErrorText raw={error} />
        </div>
      )}
      {!data && !error && <div className="empty">Loading…</div>}
      {data && !projects.length && <div className="empty">No indexes on this server yet.</div>}
      {projects.map((p) => (
        <ProjectBlock key={p.name || "(none)"} p={p} info={info} />
      ))}
    </AppShell>
  );
}

function ProjectBlock({ p, info }: { p: Project; info: Map<string, IndexInfo> }) {
  // A project with no namespace is not a project. It is whatever was indexed
  // without one, and calling it a project would invent a thing that does not
  // exist, so it is named for what it is and has no project page.
  const unnamespaced = !p.name;
  const indexes = p.indexes ?? [];

  return (
    <section className="panel projblock">
      <div className="projblock-head">
        <h3>
          {unnamespaced ? (
            "Other indexes"
          ) : (
            <Link to="/p/$project" params={{ project: p.name }}>
              {p.name}
            </Link>
          )}
        </h3>
        <span className="facts">
          {documents(p.documents ?? 0)} · {passages(p.fragments ?? 0)}
        </span>
      </div>
      {/* Absent is not "not watching": a project whose folder the daemon was
          never told cannot be watched and has not declined to be. The folder
          is shown without the home prefix (F4). */}
      {p.home && (
        <div className="projblock-home">
          {p.watching ? "Watching for changes in " : "Folder (not watched): "}
          {displayPath(p.home)}
        </div>
      )}
      <IndexRows indexes={indexes} info={info} />
    </section>
  );
}

export function rootSections(projects: Project[]): NavSection[] {
  const loose = projects.find((p) => !p.name)?.indexes ?? [];
  return [
    { heading: "", items: [{ label: "All projects", to: "/", exact: true }] },
    {
      heading: "Projects",
      items: projects
        .filter((p) => p.name)
        .map((p) => ({ label: p.name, to: `/p/${p.name}`, count: p.failed })),
    },
    {
      heading: "Other indexes",
      items: loose.map((i) => ({ label: i.local, to: `/i/${i.name}`, count: i.failed })),
    },
    settingsSection,
  ];
}
