import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useState } from "react";

import { DocProvider, useDocDetail } from "../useDocDetail";
import { Retitle } from "../panes/Retitle";
import { DocActions } from "../panes/DocActions";

// Everything below /i/:index/d/:doc — the breadcrumb, the title block and the
// sub-tab bar — with the sub-route rendered into the Outlet.
//
// The document is fetched ONCE here and handed down through the outlet context.
// /api/doc-detail already assembles pages, transcript, near-duplicates, history
// and review state into one answer, so a per-sub-tab fetch would be four
// requests for a response the shell already holds, and four chances for the
// sub-tabs to disagree about what the document is.
export function DocShell() {
  const { index, doc } = useParams({ from: "/i/$index/d/$doc" });
  const { detail, error, reload } = useDocDetail(index, doc);
  const [editing, setEditing] = useState(false);

  const identity = detail?.identity;
  // What a person said beats what a machine said, and the machine's caption
  // beats the filename. Showing the filename when somebody has corrected it is
  // the exact failure this feature was asked for.
  const title = identity?.name || detail?.title || doc.split("/").pop() || doc;
  const byPerson = identity?.source === "person";

  return (
    <>
      <nav className="crumb">
        <Link to="/i/$index" params={{ index }}>
          {index}
        </Link>
        <span className="sep">/</span>
        <Link to="/i/$index/d" params={{ index }}>
          documents
        </Link>
        <span className="sep">/</span>
        <span>{doc.split("/").pop()}</span>
      </nav>

      <div className="doctitle">
        <h2>{title}</h2>
        <div className="path">{doc}</div>
        <div className="bykind">
          {identity?.kind && <span className="badge">{identity.kind}</span>}
          {/* Who said so is not decoration. A machine's caption is a paraphrase
              and a person's is a ruling, and a reader deciding whether to trust
              the title needs to know which one they are reading. */}
          {identity?.name && (
            <span className="muted">
              titled by {byPerson ? "a person" : identity.model || "a machine"}
            </span>
          )}
          <button onClick={() => setEditing((v) => !v)}>
            {editing ? "Cancel" : "Re-title"}
          </button>
        </div>
        {/* Said, not shown as fact: a summary is a READING of the document, like
            a transcription, and who made it decides how much weight it carries. */}
        {identity?.summary && (
          <div style={{ marginTop: 4, maxWidth: "70ch" }}>{identity.summary}</div>
        )}
        {editing && (
          <Retitle
            index={index}
            doc={doc}
            current={identity}
            onDone={() => {
              setEditing(false);
              reload();
            }}
          />
        )}
        {detail && <DocActions index={index} doc={doc} detail={detail} onDone={reload} />}
      </div>

      <div className="panel" style={{ marginTop: 12 }}>
        <nav className="subtabs">
          {/* Counts in the labels, as the page this replaces had them: they are
              what tells you a tab is worth opening before you open it. */}
          <Link to="/i/$index/d/$doc/pages" params={{ index, doc }}
                activeProps={{ className: "on" }}>
            Pages{count(detail?.pages?.length)}
          </Link>
          <Link to="/i/$index/d/$doc/transcript" params={{ index, doc }}
                activeProps={{ className: "on" }}>
            Transcript
          </Link>
          <Link to="/i/$index/d/$doc/seen" params={{ index, doc }}
                activeProps={{ className: "on" }}>
            Seen in{count(detail?.seen_in?.length)}
          </Link>
          <Link to="/i/$index/d/$doc/history" params={{ index, doc }}
                activeProps={{ className: "on" }}>
            History{count(detail?.history?.length)}
          </Link>
          <Link to="/i/$index/d/$doc/notes" params={{ index, doc }}
                activeProps={{ className: "on" }}>
            Notes
          </Link>
          {/* The attest mount addresses assets ROOT-RELATIVE, so this takes
              detail.asset and not the absolute path the route carries. Passing
              the route's own `doc` here 404'd every review link. */}
          {detail?.asset && (
            <Link to="/i/$index/attest/a/$asset" params={{ index, asset: detail.asset }}
                  activeProps={{ className: "on" }}>
              Review
            </Link>
          )}
        </nav>

        {error && <div className="empty err">{error}</div>}
        {!error && !detail && <div className="empty">loading…</div>}
        {detail && (
          <DocProvider value={{ index, doc, detail, reload }}>
            <Outlet />
          </DocProvider>
        )}
      </div>
    </>
  );
}

// A zero is not shown. "Seen in (0)" and "Seen in" say the same thing, and the
// parenthesis costs a reader a glance to work that out.
function count(n?: number): string {
  return n ? ` (${n})` : "";
}
