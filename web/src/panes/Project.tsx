import { useParams } from "@tanstack/react-router";

import { useEffect, useState } from "react";

import { listProjects, projectSavings, type Project as ProjectRow, type ProjectSavings } from "../api";
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

      <SavingsBanner ns={p.name} />

      <h2 className="scopesec">Indexes</h2>
      <div className="panel">
        <IndexRows indexes={indexes} info={info} />
      </div>
    </>
  );
}

// Shown only when several people's layers share a base nobody maintains, and
// copies between them cost something. Absent for every other project: the
// endpoint answers 404 for a project that was not registered by id.
function SavingsBanner({ ns }: { ns: string }) {
  const [s, setS] = useState<ProjectSavings | null>(null);
  useEffect(() => {
    let live = true;
    projectSavings(ns)
      .then((r) => live && setS(r))
      .catch(() => live && setS(null));
    return () => {
      live = false;
    };
  }, [ns]);
  if (!s?.recommend) return null;
  const mb = (s.duplicate_bytes / 1e6).toFixed(1);
  const mins = Math.round(s.model_seconds / 60);
  return (
    <div className="panel savings">
      <strong>A maintained base would save this project work.</strong>
      <div>
        {s.duplicate_documents.toLocaleString()} document copies across{" "}
        {(s.layers ?? []).length} layers: {s.duplicate_passages.toLocaleString()} passages,{" "}
        {s.duplicate_embeddings.toLocaleString()} embeddings, {s.duplicate_page_reads.toLocaleString()} page
        reads, {mb} MB{mins > 0 ? `, about ${mins} min of model time` : ""}. Each search reads{" "}
        {s.fanout_now} indexes; with a maintained base it would read {s.fanout_with_base}.
      </div>
      <div className="muted" title={s.note}>
        A maintainer service owns the base and folds the public layers into it. Token counts are not recorded.
      </div>
    </div>
  );
}
