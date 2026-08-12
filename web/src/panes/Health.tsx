import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";

import { getJSON, postJSON } from "../api";
import { usePoll } from "../usePoll";

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
    "Indexed but unsearchable",
    "A document row exists and the count includes it, but it has no fragments — no search can ever return it.",
  ],
  [
    "generated-indexed",
    "raglit's own output, indexed as a document",
    "A transcription or region record written beside a real document and then indexed as if it were one. Self-feeding: indexing a transcription writes a transcription of it.",
  ],
  [
    "missing-file",
    "File is not where the index says",
    "The quietest failure here: search keeps working, because the fragments are in the database. Everything that goes back to the FILE breaks — download, re-read, page images, and writing a reading so it can be reviewed. Usually the file MOVED and is still in the tree.",
  ],
  [
    "no-pages",
    "Paged document with no pages",
    "A PDF whose pages were never recorded: page images, the transcript and every page citation have nothing behind them.",
  ],
  [
    "job-failed",
    "Failed ingest jobs",
    "The document is not in the index and nothing said so. Each row names the stage it died in.",
  ],
  [
    "segment-degraded",
    "Stored whole, not segmented",
    "The model would not segment these pages, so each came back as one undivided block. Searchable, but not retrievable at fragment grain.",
  ],
  [
    "page-unread",
    "Indexed with a hole in it",
    "The OCR could not read these pages and the ingest kept the rest rather than discarding the document. The trade is only sound if the hole is findable: search returns a partial document exactly like a whole one, and a reader takes the absence for the record's.",
  ],
  [
    "llm-retries",
    "Jobs the endpoint made fight",
    "These completed, but only after retrying. The earliest warning that a failure is coming.",
  ],
  [
    "withdrawn",
    "Withdrawn on purpose",
    "Ruled out of the corpus with grounds — absent by decision, not by accident.",
  ],
];

export function Health() {
  const { index } = useParams({ from: "/i/$index" });
  const { data, error, refresh } = usePoll<{ problems?: Problem[] }>(
    () => getJSON("/api/problems", { index }),
    5000,
    [index],
  );

  if (error) return <div className="empty err">{error}</div>;
  if (!data) return <div className="empty">loading…</div>;
  const problems = data.problems ?? [];
  if (!problems.length) return <div className="empty">Nothing wrong with this index.</div>;

  // Anything the list above does not name still gets shown.
  //
  // The list was a filter, so a kind it did not know about was DROPPED — and one
  // was: `page-unread`, which reports the salvage holes, the pages an ingest
  // failed to read in documents it kept anyway. Two of them sat in the delano
  // index, the endpoint reported them on every poll, and the tab rendered
  // neither. A view whose job is to surface what is wrong must not be able to
  // silently discard a problem because nobody updated a constant.
  const known = new Set(KINDS.map(([k]) => k));
  const unknown = [...new Set(problems.map((p) => p.kind))].filter((k) => !known.has(k));
  const groups: [kind: string, title: string, why: string][] = [
    ...KINDS,
    ...unknown.map(
      (k): [string, string, string] => [
        k,
        k,
        "This daemon reports a problem kind this page does not have wording for — it is newer than the page. The rows are shown as the endpoint sent them.",
      ],
    ),
  ];

  return (
    <>
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
              {title} ({mine.length})
            </h3>
            <p className="why">{why}</p>
            {shared && <p className="why">reason: {shared}</p>}
            {mine.map((p, i) => (
              <ProblemRow
                key={`${p.subject}-${p.job_id ?? i}`}
                p={p}
                index={index}
                shared={!!shared}
                onDone={refresh}
              />
            ))}
          </div>
        );
      })}
    </>
  );
}

function ProblemRow({
  p,
  index,
  shared,
  onDone,
}: {
  p: Problem;
  index: string;
  shared: boolean;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

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
  const reread = () => run(() => postJSON("/api/reread", { index, path: p.subject }));
  const ingestFresh = () =>
    run(() => postJSON("/ingest", { index }, { targets: [p.subject], fresh: true }));
  const restore = () =>
    run(() => postJSON("/api/restore", { index, path: p.subject, by: whoami() }));
  const withdraw = () => {
    // The grounds ARE the withdrawal. Asking rather than defaulting is the
    // point: a stock reason nobody wrote reads like a decision.
    const reason = prompt(
      "Grounds for withdrawing this document.\n\nIt stays on disk; it leaves the searchable corpus and stays out across re-ingest.",
      "",
    );
    if (reason === null) return;
    if (!reason.trim()) {
      setErr("a withdrawal needs grounds");
      return;
    }
    run(() =>
      postJSON("/api/withdraw", { index, path: p.subject, reason, by: whoami() }),
    );
  };

  const forget = () =>
    run(() => postJSON("/api/forget", { index, path: p.subject }));

  const actionsFor = (): [string, () => void][] => {
    switch (p.kind) {
      // A missing file is offered "Forget" and NOT "Re-ingest": there is nothing
      // at the path to ingest. Forget is second, behind nothing, because it is
      // the destructive half — the file has usually moved, and dropping the row
      // before indexing the copy that still exists is how a renamed document
      // becomes unfindable rather than merely unopenable.
      case "missing-file":
        return [["Forget this row", forget]];
      case "generated-indexed":
        return [["Forget", forget]];
      case "no-fragments":
        return [["Re-ingest", ingestFresh], ["Withdraw…", withdraw]];
      case "no-pages":
        return [["Re-read", reread], ["Withdraw…", withdraw]];
      case "segment-degraded":
        return [["Re-read", reread]];
      // A deterministic refusal re-reads to the same refusal — the repetition
      // guard at temp 0 returns the identical result on identical pixels — so
      // Re-read is offered and is NOT the whole answer. `raglit regions` is,
      // and it is on the row as the fix text.
      case "page-unread":
        return [["Re-read", reread]];
      case "job-failed":
        return p.job_id
          ? [["Retry", () => job("retry")], ["Forget", () => job("forget")]]
          : [];
      case "withdrawn":
        return [["Restore", restore]];
      // A retry warning is about a job that SUCCEEDED, so it offered no repair
      // and no way to see what it was reporting: the row named an endpoint that
      // had struggled and stopped there. Re-ingest re-runs the pipeline (page
      // OCR still comes from the cache, so the pixels are not read twice);
      // Dismiss drops the job record, which is what makes the warning stop. The
      // account itself is one click away, on the job.
      case "llm-retries":
        return p.job_id
          ? [["Re-ingest", () => job("retry")], ["Dismiss", () => job("forget")]]
          : [];
      default:
        return [];
    }
  };
  const actions = actionsFor();

  // A row offers BOTH destinations, and each one goes where its name says.
  //
  // The path used to link at the JOB for the two job-shaped kinds, so clicking a
  // document opened a job table — and, since that table was the newest 200 rows
  // and a cited job is routinely older, usually a job table without the job in
  // it. Now the path is the document and the #id badge is the job, which is
  // where the stages and the endpoint's own words live.
  return (
    <div className="prob">
      <div className="subj">
        <Link to="/i/$index/d/$doc/pages" params={{ index, doc: p.subject }}>
          {p.subject}
        </Link>
        {p.job_id != null && p.job_id > 0 && (
          <Link
            className="badge"
            style={{ marginLeft: ".4rem" }}
            title="open this ingest job: its stages, timings, and what each one said"
            to="/i/$index/jobs/$jobId"
            params={{ index, jobId: String(p.job_id) }}
          >
            #{p.job_id}
          </Link>
        )}
        {p.stage && <span className="badge error" style={{ marginLeft: ".4rem" }}>{p.stage}</span>}
      </div>
      {/* Verbatim. Summarising the endpoint's own words is how "increase the
          physical batch size" became "status 500" and cost a week. */}
      {p.detail && !shared && <div className="det">{p.detail}</div>}
      {(actions.length > 0 || p.fix) && (
        <div className="acts">
          {actions.map(([label, fn]) => (
            <button className="act" key={label} disabled={busy} onClick={fn}>
              {label}
            </button>
          ))}
          {p.fix && (
            <span
              className="fix"
              title="the same thing on the command line — click to copy"
              onClick={() => navigator.clipboard?.writeText(p.fix!)}
            >
              {p.fix}
            </span>
          )}
        </div>
      )}
      {err && <div className="err">{err}</div>}
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
