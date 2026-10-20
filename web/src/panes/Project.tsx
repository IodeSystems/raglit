import { useParams } from "@tanstack/react-router";

import { listProjects, type Project as ProjectRow } from "../api";
import { usePoll } from "../usePoll";
import { ErrorText } from "../ui/Details";
import { displayPath, documents, passages } from "../ui/display";
import { IndexRows, useIndexInfo } from "./IndexRows";
import "./scope.css";

// One project: the indexes it owns, with its branches under the index each
// overlays rather than beside it. The same index rows as the overview, so a
// project page is that page's card, opened up.
export function Project() {
  const { project } = useParams({ from: "/p/$project" });
  const { data, error } = usePoll<{ projects?: ProjectRow[] }>(listProjects, 5000, []);
  const info = useIndexInfo();

  if (error) {
    return (
      <div className="empty">
        <ErrorText raw={error} />
      </div>
    );
  }
  if (!data) return <div className="empty">Loading…</div>;
  const p = (data.projects ?? []).find((x) => x.name === project);
  if (!p) {
    return (
      <div className="empty">
        There is no project “{project}” on this server. A project exists because
        an index carries its name; nothing creates one on its own.
      </div>
    );
  }

  const indexes = p.indexes ?? [];
  const busy = (p.pending ?? 0) + (p.running ?? 0);

  return (
    <>
      <div className="scopehead">
        <h1>{p.name}</h1>
        <div className="facts">
          {documents(p.documents ?? 0)}
          <span className="sep">·</span>
          {passages(p.fragments ?? 0)}
          {busy > 0 && (
            <>
              <span className="sep">·</span>
              {busy.toLocaleString()} importing
            </>
          )}
        </div>
        {p.home && (
          <div className="facts">
            {p.watching ? "Watching for changes in " : "Folder (not watched): "}
            {displayPath(p.home)}
            {p.files ? ` · ${p.files.toLocaleString()} source files` : ""}
          </div>
        )}
      </div>

      <h2 className="scopesec">Indexes</h2>
      <div className="panel">
        <IndexRows indexes={indexes} info={info} />
      </div>
    </>
  );
}
