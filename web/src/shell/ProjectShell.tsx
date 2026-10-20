import { Outlet, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { listProjects, type Project } from "../api";
import { AppShell, CrumbLink, ShellSearch } from "./AppShell";
import type { NavSection } from "./NavDrawer";

// Everything below /p/:project — the breadcrumb, the project-wide search box and
// the sidebar — kept mounted while the routes underneath change.
//
// Nested for the same reason IndexShell is: the search box lives HERE, so it
// holds its text and focus while you walk from a hit into a document and back.
// Same frame as the index pages (AppShell); only the sidebar entries differ.
export function ProjectShell() {
  const { project } = useParams({ from: "/p/$project" });
  const [p, setP] = useState<Project | undefined>();

  useEffect(() => {
    let live = true;
    listProjects()
      .then((got) => live && setP((got.projects ?? []).find((x) => x.name === project)))
      .catch(() => live && setP(undefined));
    return () => {
      live = false;
    };
  }, [project]);

  return (
    <AppShell
      crumbs={[<CrumbLink to="/">Projects</CrumbLink>, project]}
      search={
        <ShellSearch
          placeholder={`Search every index in ${project}`}
          onSearch={(q, nav) =>
            nav({ to: "/p/$project/search", params: { project }, search: { q, mode: "bm25" } })
          }
        />
      }
      nav={projectSections(project, p)}
    >
      <Outlet />
    </AppShell>
  );
}

export function projectSections(project: string, p?: Project): NavSection[] {
  const indexes = p?.indexes ?? [];
  return [
    {
      heading: "Project",
      items: [
        { label: "Indexes", to: `/p/${project}`, exact: true },
        { label: "Search", to: `/p/${project}/search` },
      ],
    },
    {
      heading: "Indexes",
      items: indexes.map((i) => ({
        label: i.local,
        to: `/i/${i.name}`,
        mark: i.parent ? "⑂" : undefined,
        title: i.parent ? `Branch of ${i.parent}` : undefined,
        count: i.failed,
      })),
    },
  ];
}
