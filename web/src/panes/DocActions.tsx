import { useState } from "react";

import { postJSON, type DocDetail } from "../api";
import { ErrorText } from "../ui/Details";
import { fileName } from "../ui/display";
import { useOperator } from "../ui/operator";

// What can be done to the document itself, as opposed to what can be said about
// it.
//
// Two different re-reads, named apart ("Re-index" and "Re-read pages") because they are not the same act and the
// difference is what you get back. Re-ingest re-fragments and re-indexes from
// the file as it stands; re-read purges the cached page OCR first, which is the
// one that matters when the CACHE is what is wrong — a re-index alone returns
// the same answer from the same cache.
export function DocActions({
  index,
  doc,
  detail,
  onDone,
}: {
  index: string;
  doc: string;
  detail: DocDetail;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  // Re-index and re-read are how the index is RUN, and beside the title they
  // read to a reader as an admin panel (focus groups, 2026-10-24). Shown with
  // operator tools on; downloading the file is a reader's act and stays.
  const operator = useOperator();
  // Work already queued DISABLES both, and says why. The queue does not dedupe,
  // so a button that stays live while a job runs is an invitation to start the
  // same minutes-long VLM pass three times.
  const inFlight = detail.in_flight ?? [];
  const blocked = inFlight.length > 0;

  const act = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    setErr("");
    try {
      await fn();
      onDone();
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy("");
    }
  };

  // The tooltip says what the button does; when it is disabled, why.
  const why = blocked
    ? `Work for this document is already ${stateWord(inFlight[0]!.state)}. Wait for it to finish.`
    : undefined;

  return (
    <div className="docacts">
      {detail.original && (
        // The filename MUST be given. An empty download attribute makes the
        // browser derive it from the URL path, which here ends "/source" — so
        // the file arrived named "source", with no extension, and looked like a
        // broken link rather than a working one with a bad name.
        <a
          href={detail.original}
          download={fileName(detail.path || doc)}
          title="Download the file exactly as it was added"
        >
          Download original
        </a>
      )}
      {operator && (
        <>
          <button
            disabled={blocked || !!busy}
            title={why ?? "Add this document to the index again from the file as it is now"}
            onClick={() =>
              // fresh:true, not a plain re-ingest. Without it the unchanged-bytes
              // fast path and the cross-index pool both short-circuit, so pressing
              // "re-ingest" on a file nobody edited does nothing at all — which
              // looks identical to a button that is broken.
              void act("ingest", () => postJSON("/ingest", { index }, { targets: [doc], fresh: true }))
            }
          >
            {busy === "ingest" ? "Queued…" : "Re-index"}
          </button>
          <button
            disabled={blocked || !!busy}
            title={
              why ?? "Read the page images again from scratch, discarding the saved reading of every page"
            }
            // /api/reread takes `path` as a QUERY parameter only; sent in the body it
            // was a 422. The display path is accepted like the absolute one.
            onClick={() => void act("reread", () => postJSON("/api/reread", { index, path: detail.path || doc }))}
          >
            {busy === "reread" ? "Queued…" : "Re-read pages"}
          </button>
        </>
      )}
      {blocked && (
        <span className="badge vision" title={inFlight.map((j) => `job #${j.id}: ${j.state}`).join("\n")}>
          {inFlight.length === 1
            ? `Work ${stateWord(inFlight[0]!.state)}`
            : `${inFlight.length} jobs ${stateWord(inFlight[0]!.state)}`}
        </span>
      )}
      {err && <ErrorText raw={err} />}
    </div>
  );
}

// The queue's states, in words: "pending" reads as a problem to somebody who
// has not seen the queue.
function stateWord(state: string): string {
  if (state === "pending") return "waiting";
  if (state === "running") return "in progress";
  return state;
}
