import { Link, useParams } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";

import { getJSON, postJSON } from "../api";
import { usePoll } from "../usePoll";
import { Details, ErrorText } from "../ui/Details";
import { documents, fileName } from "../ui/display";
import { docParam, docRel, isRetryTally, retryEach, retryJob, retryText, stageLabel, useIndexRoot } from "./opsText";
import "./ops.css";

type Problem = {
  kind: string;
  subject: string;
  job_id?: number;
  stage?: string;
  detail?: string;
  fix?: string;
};

// What is wrong with this index, grouped by what KIND of wrong it is, with the
// grounds spelled out once per group.
//
// Ordering and wording are carried over from the page this replaces, including
// why each group says what it says. The repair action comes first and the
// give-up last, so the button under the cursor is the one that fixes it.
const KINDS: [kind: string, title: string, why: string][] = [
  [
    "no-fragments",
    "Not searchable",
    "These documents are in the index but have no searchable passages, so no search can return them.",
  ],
  [
    "generated-indexed",
    "Generated files indexed as documents",
    "Transcriptions or page records that raglit wrote next to a document were indexed as if they were documents themselves. Remove them from the index.",
  ],
  [
    "missing-file",
    "File not at its recorded location",
    "Search still works, because the passages are stored in the index. Download, re-reading, page images and review need the file itself. The file has usually been moved or renamed.",
  ],
  [
    "no-pages",
    "PDFs with no page records",
    "Page images, the transcript and page references are not available for these documents.",
  ],
  [
    "job-failed",
    "Failed imports",
    "These documents could not be imported and are not in the index. Each row names the step that failed.",
  ],
  [
    "segment-degraded",
    "Pages stored without passage breaks",
    "These pages could not be split into passages, so each page is stored as one block. They are searchable, but a result points to the whole page.",
  ],
  [
    "empty-source",
    "Empty files",
    "The file on disk has no content. This is usually a damaged copy or an incomplete export, not a failed import.",
  ],
  [
    "page-unread",
    "Documents with unreadable pages",
    "Some pages could not be read and the rest of the document was kept. Search results from these documents can be incomplete without showing it.",
  ],
  [
    "page-partial",
    "Documents with partly read pages",
    "The end of these pages could not be read; the part that was read was kept. Search results from these pages can be incomplete.",
  ],
  [
    "llm-retries",
    "Completed after retries",
    "These imports finished, but only after the AI service was retried. Frequent retries are an early sign of failures.",
  ],
  [
    "withdrawn",
    "Withdrawn",
    "Removed from search on purpose, with a recorded reason.",
  ],
];

export function Health() {
  const { index } = useParams({ from: "/i/$index" });
  const { root, ready } = useIndexRoot(index);
  const { data, error, refresh } = usePoll<{ problems?: Problem[] }>(
    () => getJSON("/api/problems", { index }),
    5000,
    [index],
  );

  if (error)
    return (
      <div className="empty ops">
        The list of issues could not be loaded.
        <ErrorText raw={error} />
      </div>
    );
  if (!data || !ready) return <div className="empty">Loading…</div>;
  const problems = data.problems ?? [];
  // Said as a finding, with what was checked: a lone sentence in an empty page
  // read as unfinished (focus groups, 2026-10-24), and "no issues" means more
  // when the reader can see what would have been one.
  if (!problems.length)
    return (
      <div className="panel health-ok">
        <div className="health-ok-title">Nothing needs attention</div>
        <p>
          Every document in this index was read, has searchable text, and still has its file. When an
          import fails or a file moves, it is listed here with what to do about it.
        </p>
      </div>
    );

  // Anything the list above does not name still gets shown.
  //
  // The list was a filter, so a kind it did not know about was DROPPED — and one
  // was: `page-unread`, which reports the salvage holes, the pages an ingest
  // failed to read in documents it kept anyway. A view whose job is to surface
  // what is wrong must not be able to silently discard a problem because nobody
  // updated a constant.
  const known = new Set(KINDS.map(([k]) => k));
  const unknown = [...new Set(problems.map((p) => p.kind))].filter((k) => !known.has(k));
  const groups: [kind: string, title: string, why: string][] = [
    ...KINDS,
    ...unknown.map(
      (k): [string, string, string] => [
        k,
        k.charAt(0).toUpperCase() + k.slice(1).replace(/-/g, " "),
        "The server reports this kind of issue, but this page has no description for it yet.",
      ],
    ),
  ];

  return (
    <div className="ops">
      {groups.map(([kind, title, why]) => {
        const mine = problems.filter((p) => p.kind === kind);
        if (!mine.length) return null;
        // One set of grounds, shown once. Twenty-six rows repeating the same
        // sentence is how the section a reader should skim becomes a wall.
        const shared =
          mine.length > 1 && mine.every((x) => x.detail === mine[0]!.detail)
            ? mine[0]!.detail
            : "";
        return (
          <div className="probgroup panel" key={kind}>
            <h3>
              {title} <span className="count">{documents(mine.length)}</span>
            </h3>
            <p className="why">{why}</p>
            {kind === "job-failed" && <RetryAll index={index} problems={mine} onDone={refresh} />}
            {shared && <ProblemDetail kind={kind} detail={shared} />}
            {mine.map((p, i) => (
              <ProblemRow
                key={`${p.subject}-${p.job_id ?? i}`}
                p={p}
                index={index}
                root={root}
                shared={!!shared}
                onDone={refresh}
              />
            ))}
          </div>
        );
      })}
    </div>
  );
}

// One button for the whole group: a failed import is retried far more often
// than it is investigated one row at a time.
function RetryAll({ index, problems, onDone }: { index: string; problems: Problem[]; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const ids = problems.map((p) => p.job_id).filter((id): id is number => !!id && id > 0);
  if (ids.length < 2) return null;
  const go = async () => {
    setBusy(true);
    setNote("");
    const failed = await retryEach(ids, (id) => retryJob(index, id));
    setNote(failed ? `${failed.toLocaleString()} could not be retried.` : "Retrying.");
    setBusy(false);
    onDone();
  };
  return (
    <div className="ops-head">
      <button className="act primary" disabled={busy} onClick={() => void go()}>
        {busy ? "Retrying…" : `Retry all ${ids.length.toLocaleString()}`}
      </button>
      {note && <span className="ops-note">{note}</span>}
    </div>
  );
}

// A problem's grounds as a person reads them. A retry tally is rewritten into
// words; anything else is humanized. The daemon's own text is kept, verbatim,
// in the disclosure — summarising it away is how "increase the physical batch
// size" once became "status 500".
function ProblemDetail({ kind, detail, extra }: { kind: string; detail: string; extra?: ReactNode }) {
  if (kind === "llm-retries" || isRetryTally(detail)) {
    return (
      <div className="errline">
        <div className="ops-note">{retryText(detail)}</div>
        <Details>
          <pre>{detail}</pre>
          {extra}
        </Details>
      </div>
    );
  }
  // Page lists: "unread page(s): 3 9" (problems.go) → a sentence.
  const pagesMatch = detail.match(/^(unread|salvaged) page\(s\)[^:]*:\s*(.+)$/);
  if (pagesMatch) {
    const list = pagesMatch[2]!.trim().split(/\s+/).join(", ");
    return (
      <div className="errline">
        <div className="ops-note">
          {pagesMatch[1] === "unread" ? "Pages that could not be read" : "Pages only partly read"}: {list}
        </div>
        {extra && <Details>{extra}</Details>}
      </div>
    );
  }
  // A fixed explanation, not evidence: the group's description already says it
  // in plain words, so the daemon's sentence is only in the disclosure.
  if (kind === "missing-file" || kind === "empty-source") {
    return (
      <Details>
        <pre>{detail}</pre>
        {extra}
      </Details>
    );
  }
  // These kinds' detail is a description of the document, not an error.
  if (kind === "withdrawn") {
    return (
      <div className="errline">
        <div className="ops-note">Reason: {detail}</div>
        {extra && <Details>{extra}</Details>}
      </div>
    );
  }
  return <ErrorText raw={detail} extra={extra} />;
}

function ProblemRow({
  p,
  index,
  root,
  shared,
  onDone,
}: {
  p: Problem;
  index: string;
  root?: string;
  shared: boolean;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  // Sent back to the daemon as the display path; the middleware resolves it.
  const doc = docParam(p.subject, root);
  const rel = docRel(p.subject, root);

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setErr("");
    try {
      await fn();
      onDone();
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  const job = (kind: "retry" | "forget") =>
    run(() => postJSON("/api/jobs/" + kind, { index }, { id: p.job_id }));
  const reread = () => run(() => postJSON("/api/reread", { index, path: doc }));
  // /ingest takes the indexed path: it is not behind the display-path middleware.
  const ingestFresh = () =>
    run(() => postJSON("/ingest", { index }, { targets: [p.subject], fresh: true }));
  const restore = () =>
    run(() => postJSON("/api/restore", { index, path: doc, by: whoami() }));
  const withdraw = () => {
    // The grounds ARE the withdrawal. Asking rather than defaulting is the
    // point: a stock reason nobody wrote reads like a decision.
    const reason = prompt(
      "Reason for withdrawing this document.\n\nThe file stays on disk. It is removed from search and stays out when the index is rebuilt.",
      "",
    );
    if (reason === null) return;
    if (!reason.trim()) {
      setErr("A withdrawal needs a reason.");
      return;
    }
    void run(() => postJSON("/api/withdraw", { index, path: doc, reason, by: whoami() }));
  };

  const forget = () => run(() => postJSON("/api/forget", { index, path: doc }));

  // The first action is the repair, the last the give-up, so the button under
  // the cursor is the one that fixes it.
  const actionsFor = (): [string, () => void][] => {
    switch (p.kind) {
      // A missing file is offered "Remove" and NOT "Re-import": there is nothing
      // at the path to import. The file has usually moved, and dropping the row
      // before indexing the copy that still exists is how a renamed document
      // becomes unfindable rather than merely unopenable.
      case "missing-file":
        return [["Remove from index", forget]];
      case "generated-indexed":
        return [["Remove from index", forget]];
      case "no-fragments":
        return [["Import again", ingestFresh], ["Withdraw…", withdraw]];
      case "no-pages":
        return [["Read again", reread], ["Withdraw…", withdraw]];
      case "segment-degraded":
        return [["Read again", reread]];
      // Nothing to re-read — there are no bytes. Either the file gets replaced
      // and re-imported, or the absence is recorded as a decision.
      case "empty-source":
        return [["Import again", ingestFresh], ["Withdraw…", withdraw]];
      // A deterministic refusal re-reads to the same refusal, so Read again is
      // offered and is NOT the whole answer. `raglit regions` is, and it is in
      // the row's technical details.
      case "page-unread":
        return [["Read again", reread]];
      case "job-failed":
        return p.job_id
          ? [["Retry", () => job("retry")], ["Dismiss", () => job("forget")]]
          : [];
      case "withdrawn":
        return [["Restore", restore]];
      // A retry warning is about a job that SUCCEEDED. Import again re-runs the
      // pipeline (page OCR still comes from the cache); Dismiss drops the job
      // record, which is what makes the warning stop.
      case "llm-retries":
        return p.job_id
          ? [["Import again", () => job("retry")], ["Dismiss", () => job("forget")]]
          : [];
      default:
        return [];
    }
  };
  const actions = actionsFor();

  // The CLI equivalent belongs to whoever has to fix it from a shell, so it is
  // in the disclosure, not in the row.
  const fix = p.fix ? (
    <div>
      Command line:{" "}
      <code
        title="Click to copy"
        style={{ cursor: "copy" }}
        onClick={() => void navigator.clipboard?.writeText(p.fix!)}
      >
        {p.fix}
      </code>
    </div>
  ) : undefined;

  // A row offers BOTH destinations, and each one goes where its name says: the
  // name opens the document, "Import log" opens the job's stages.
  const name = fileName(rel);
  return (
    <div className="prob">
      <div className="subj">
        <Link className="doc-name" to="/i/$index/d/$doc/pages" params={{ index, doc }}>
          {name}
        </Link>
        {p.stage && p.kind === "job-failed" && (
          <span className="badge error" style={{ marginLeft: ".5rem" }}>
            Failed at: {stageLabel(p.stage)}
          </span>
        )}
      </div>
      {rel !== name && <div className="doc-path">{rel}</div>}
      {p.detail && !shared ? (
        <ProblemDetail kind={p.kind} detail={p.detail} extra={fix} />
      ) : (
        fix && <Details>{fix}</Details>
      )}
      {(actions.length > 0 || (p.job_id != null && p.job_id > 0)) && (
        <div className="acts">
          {actions.map(([label, fn], i) => (
            <button
              className={"act" + (i === 0 && label === "Retry" ? " primary" : "")}
              key={label}
              disabled={busy}
              onClick={fn}
            >
              {label}
            </button>
          ))}
          {p.job_id != null && p.job_id > 0 && (
            <Link
              className="act"
              title="The import's steps, timings and messages"
              to="/i/$index/jobs/$jobId"
              params={{ index, jobId: String(p.job_id) }}
            >
              Import log
            </Link>
          )}
        </div>
      )}
      {err && <ErrorText raw={err} />}
    </div>
  );
}

function whoami(): string {
  try {
    return localStorage.getItem("raglit.author") || localStorage.getItem("raglit.by") || "";
  } catch {
    return "";
  }
}
