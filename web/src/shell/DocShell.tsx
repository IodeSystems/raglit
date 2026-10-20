import { Link, Outlet, useNavigate, useParams, useRouter } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { DocProvider, useDocDetail } from "../useDocDetail";
import { isMailArchive } from "../api";
import { Retitle } from "../panes/Retitle";
import { DocActions } from "../panes/DocActions";
import { ErrorText } from "../ui/Details";
import { AISummaryLabel, AITitleLabel } from "../ui/Label";
import { displayPath, fileName } from "../ui/display";
import "./DocShell.css";

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
  const navigate = useNavigate();
  const router = useRouter();

  // The visible path is the one RELATIVE to the index root (F4). The daemon
  // sends it as detail.path; before it lands, the route's own value is cut down
  // the same way so the home directory never shows, not even for a moment.
  const shownPath = detail?.path || displayPath(doc, detail?.root);

  // A link from somewhere older may still carry the absolute path. Once the
  // daemon has said what the display path is, swap it into the URL in place, so
  // the address bar does not show /home/<user>/… either. Every document endpoint
  // takes the display path, so the sub-tab keeps working on the new URL.
  useEffect(() => {
    const rel = detail?.path;
    if (!rel || rel === doc || !doc.startsWith("/") || rel.split("/").includes("..")) return;
    // The deepest match names the sub-tab's route template; re-filling it with
    // the new `doc` keeps the reader on the same tab and page.
    const leaf = router.state.matches[router.state.matches.length - 1];
    if (!leaf) return;
    void navigate({
      to: leaf.fullPath,
      params: { ...(leaf.params as Record<string, string>), doc: rel },
      search: leaf.search,
      replace: true,
    } as never);
  }, [detail?.path, doc, navigate, router]);

  const identity = detail?.identity;
  // What a person said beats what a machine said, and the machine's caption
  // beats the filename. Showing the filename when somebody has corrected it is
  // the exact failure this feature was asked for.
  const title = identity?.name || detail?.title || fileName(shownPath) || shownPath;
  const byPerson = identity?.source === "person";

  return (
    <>
      {/* The app bar already names the project and index; this trail only
          adds the way back to the list. */}
      <nav className="crumb">
        <Link to="/i/$index/d" params={{ index }}>
          Documents
        </Link>
        <span className="sep">/</span>
        <span>{fileName(shownPath)}</span>
      </nav>

      <div className="doctitle">
        <h2>{title}</h2>
        <div className="path" title={shownPath}>{shownPath}</div>
        <div className="bykind">
          {identity?.kind && <span className="badge">{identity.kind}</span>}
          {/* Who said so is not decoration. A machine's caption is a paraphrase
              and a person's is a ruling, and a reader deciding whether to trust
              the title needs to know which one they are reading. */}
          {identity?.name &&
            (byPerson ? (
              <span className="muted" title="A person wrote this title; a machine re-run will not replace it">
                Title set by a person
              </span>
            ) : (
              <AITitleLabel model={identity.model} />
            ))}
          <button
            className="docshell-edit"
            title="Correct the title, kind and summary. A title you set is kept when the document is read again."
            onClick={() => setEditing((v) => !v)}
          >
            {editing ? "Cancel" : "Edit title"}
          </button>
        </div>
        {/* Said, not shown as fact: a summary is a READING of the document, like
            a transcription, and who made it decides how much weight it carries. */}
        {identity?.summary && (
          <div className="docshell-summary">
            {!byPerson && <AISummaryLabel />}
            <div>{identity.summary}</div>
          </div>
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
          {/* First, and only for an archive: it is how the document is read.
              The Pages tab stays available — it is what the index actually
              holds, and a reader checking a citation wants exactly that. */}
          {isMailArchive(doc) && (
            <Link to="/i/$index/d/$doc/email" params={{ index, doc }}
                  activeProps={{ className: "on" }}>
              Thread{count(detail?.pages?.length ? detail.pages.length - 1 : 0)}
            </Link>
          )}
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
            Similar{count(detail?.seen_in?.length)}
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

        {error && (
          <div className="empty docshell-error">
            <ErrorText raw={error} />
            <button onClick={() => reload()}>Try again</button>
          </div>
        )}
        {!error && !detail && <div className="empty">Loading document…</div>}
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
