import { Link, Outlet, useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { listIndexes, type IndexInfo } from "../api";

// Everything below /i/:index — the header, the index picker, the search box and
// the tab bar — rendered once and kept mounted while the routes underneath it
// change.
//
// This is the reason the tree is nested rather than flat. The search box lives
// HERE, so it holds its text and focus while you walk from a hit into a document
// and back out; a flat tree would remount it on every navigation and lose both.
export function IndexShell() {
  const { index } = useParams({ from: "/i/$index" });
  const navigate = useNavigate();
  const [indexes, setIndexes] = useState<IndexInfo[]>([]);
  // Split the same way the daemon does (cmd/raglit/namespace.go: nsSep is "__").
  // An index with no separator has no project — it is not "a project called
  // default", it is one nobody namespaced — so the crumb shows only the index.
  const sep = index.indexOf("__");
  const project = sep > 0 ? index.slice(0, sep) : "";
  const local = sep > 0 ? index.slice(sep + 2) : index;

  useEffect(() => {
    listIndexes()
      .then((got) => setIndexes(got.indexes ?? []))
      .catch(() => setIndexes([]));
  }, []);

  // The index is in the URL, so switching it is a navigation, not a state
  // change. That is what makes an index linkable at all.
  const switchIndex = (name: string) => {
    navigate({ to: "/i/$index", params: { index: name } });
  };

  return (
    <>
      <header>
        <h1>
          <Link to="/">raglit</Link> <span>review</span>
        </h1>
        {/* The project half of the name, as a link. `delano-v-mckinnon__default`
            is a project and an index run together, and until now the only way to
            reach the project's other indexes was to know they existed. */}
        <nav className="crumbs">
          <Link to="/">projects</Link>
          <span className="sep">/</span>
          {project ? (
            <>
              <Link to="/p/$project" params={{ project }}>
                {project}
              </Link>
              <span className="sep">/</span>
              <strong>{local}</strong>
            </>
          ) : (
            <strong>{local}</strong>
          )}
        </nav>
        <select
          title="index"
          value={index}
          onChange={(e) => switchIndex(e.target.value)}
        >
          {/* The index from the URL is always an option, even if /indexes has
              not answered yet or does not know it. Otherwise a deep link to an
              index renders a picker showing some OTHER index while the page
              below it shows the right one. */}
          {!indexes.some((i) => i.name === index) && <option value={index}>{index}</option>}
          {indexes.map((i) => (
            <option key={i.name} value={i.name}>
              {i.name}
            </option>
          ))}
        </select>
        <ShellSearch index={index} />
        <div className="grow" />
      </header>

      <main>
        <nav className="tabs">
          {/* activeOptions.exact on the dashboard only: "/i/x" is a prefix of
              every other route here, so without it every tab reads as active. */}
          <Link to="/i/$index" params={{ index }} activeProps={{ className: "on" }}
                activeOptions={{ exact: true }}>
            Dashboard
          </Link>
          <Link to="/i/$index/health" params={{ index }} activeProps={{ className: "on" }}>
            Health
          </Link>
          <Link to="/i/$index/jobs" params={{ index }} activeProps={{ className: "on" }}>
            Ingest jobs
          </Link>
          <Link to="/i/$index/d" params={{ index }} activeProps={{ className: "on" }}>
            Documents
          </Link>
          <Link to="/i/$index/search" params={{ index }} search={{ q: "", mode: "bm25" }}
                activeProps={{ className: "on" }}>
            Search
          </Link>
          <Link to="/i/$index/branches" params={{ index }} activeProps={{ className: "on" }}>
            Branches
          </Link>
          <Link to="/i/$index/attest" params={{ index }} activeProps={{ className: "on" }}>
            Review
          </Link>
        </nav>
        <Outlet />
      </main>
    </>
  );
}

// The shell search box. Submitting navigates to the search route with the query
// in the URL — so a search is a link, and the back button walks searches.
function ShellSearch({ index }: { index: string }) {
  const navigate = useNavigate();
  // Seeded from the URL so landing on a pasted /search?q=… shows that query in
  // the box rather than an empty one next to its own results.
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
          to: "/i/$index/search",
          params: { index },
          search: { q: q.trim(), mode: "bm25" },
        });
      }}
    >
      <input
        type="search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="search this index — words from the document, not a question"
        autoComplete="off"
      />
    </form>
  );
}
