import { useState } from "react";

import { postJSON, type DocDetail } from "../api";

// What can be done to the document itself, as opposed to what can be said about
// it.
//
// Two different re-reads, named apart because they are not the same act and the
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

  const why = blocked ? `already ${inFlight[0]!.state} for this document` : undefined;

  return (
    <div className="docacts">
      {detail.original && (
        // The filename MUST be given. An empty download attribute makes the
        // browser derive it from the URL path, which here ends "/source" — so
        // the file arrived named "source", with no extension, and looked like a
        // broken link rather than a working one with a bad name.
        <a href={detail.original} download={doc.split("/").pop()}>
          download original
        </a>
      )}
      <button
        disabled={blocked || !!busy}
        title={why}
        onClick={() =>
          // fresh:true, not a plain re-ingest. Without it the unchanged-bytes
          // fast path and the cross-index pool both short-circuit, so pressing
          // "re-ingest" on a file nobody edited does nothing at all — which
          // looks identical to a button that is broken.
          act("ingest", () => postJSON("/ingest", { index }, { targets: [doc], fresh: true }))
        }
      >
        {busy === "ingest" ? "queued…" : "re-ingest"}
      </button>
      <button
        disabled={blocked || !!busy}
        title={why}
        onClick={() => act("reread", () => postJSON("/api/reread", { index }, { path: doc }))}
      >
        {busy === "reread" ? "queued…" : "re-read (purge OCR cache)"}
      </button>
      {blocked && (
        <span className="badge vision">
          {inFlight.length === 1
            ? `${inFlight[0]!.state} · job #${inFlight[0]!.id}`
            : `${inFlight.length} jobs ${inFlight[0]!.state}`}
        </span>
      )}
      {err && <span className="err">{err}</span>}
    </div>
  );
}
