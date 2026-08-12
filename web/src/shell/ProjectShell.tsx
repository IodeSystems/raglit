import { Link, Outlet, useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import { useEffect, useState } from "react";

// Everything below /p/:project — the breadcrumb, the project-wide search box and
// the tab bar — kept mounted while the routes underneath change.
//
// Nested for the same reason IndexShell is: the search box lives HERE, so it
// holds its text and focus while you walk from a hit into a document and back.
export function ProjectShell() {
  const { project } = useParams({ from: "/p/$project" });

  return (
    <>
      <header>
        <h1>
          <Link to="/">raglit</Link> <span>review</span>
        </h1>
        <nav className="crumbs">
          <Link to="/">projects</Link>
          <span className="sep">/</span>
          <strong>{project}</strong>
        </nav>
        <ProjectSearchBox project={project} />
        <div className="grow" />
      </header>

      <main>
        <nav className="tabs">
          <Link
            to="/p/$project"
            params={{ project }}
            activeProps={{ className: "on" }}
            activeOptions={{ exact: true }}
          >
            Indexes
          </Link>
          <Link
            to="/p/$project/search"
            params={{ project }}
            search={{ q: "", mode: "bm25" }}
            activeProps={{ className: "on" }}
          >
            Search
          </Link>
        </nav>
        <Outlet />
      </main>
    </>
  );
}

function ProjectSearchBox({ project }: { project: string }) {
  const navigate = useNavigate();
  const urlQ = useRouterState({
    select: (s) => (s.location.search as { q?: string }).q ?? "",
  });
  const [q, setQ] = useState(urlQ);
  useEffect(() => setQ(urlQ), [urlQ]);

  return (
    <form
      className="shellsearch"
      onSubmit={(e) => {
        e.preventDefault();
        navigate({
          to: "/p/$project/search",
          params: { project },
          search: { q: q.trim(), mode: "bm25" },
        });
      }}
    >
      <input
        type="search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder={`search every index in ${project}`}
        autoComplete="off"
      />
    </form>
  );
}
