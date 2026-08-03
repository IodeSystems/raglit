import { Link, useParams } from "@tanstack/react-router";
import { useCallback, useEffect, useState } from "react";

import { ApiError, getJSON, postJSON } from "../api";
import { Progress, type Stats } from "./Attest";

type Unit = {
  id: string;
  parent?: string;
  text?: string;
  label?: string;
  locator?: { page?: number; start?: number; end?: number };
};

type UnitStatus = {
  unit: Unit;
  kind?: string;
  text?: string;
  label?: string;
  note?: string;
  by?: string;
  at?: string;
  authored?: boolean;
  swept?: boolean;
};

type StateView = {
  asset?: { id?: string; kind?: string };
  producer?: string;
  units?: UnitStatus[];
  stats?: Stats;
  orphaned?: unknown[];
};

// The verdicts a person can record on one claim. Enumerated by the server
// (attest/service.go's verdict operation), so this list is the client half of a
// closed set — a kind not in it is a 422.
//
// "retract" is not offered as a first ruling. It undoes one, and offering it
// beside the others invites somebody to retract a verdict that was never made.
const KINDS = ["confirmed", "corrected", "affirmed", "unclear", "unsupported"] as const;

// One asset under review: what the machine claimed, unit by unit, and what a
// person has ruled about each.
//
// AUDIO IS NOT HANDLED HERE. The standalone workbench (attest/ui.html) has a
// player built for scrubbing a two-hour hearing — gap-skipping, per-turn seek,
// Range-served media — and none of that is ported. An audio asset links out to
// that page rather than being shown here badly, because a review surface that
// silently cannot play the recording is worse than one that says so.
export function AttestAsset() {
  const { index, asset } = useParams({ from: "/i/$index/attest/a/$asset" });
  const base = `/api/attest/${encodeURIComponent(index)}`;
  const [state, setState] = useState<StateView | null>(null);
  const [error, setError] = useState("");
  // Distinguished from any other failure, because it is not one. attest lists
  // and serves assets WITH A MACHINE READING, so a 404 here means nothing has
  // read this document yet — a state with an action, not a broken mount.
  const [unread, setUnread] = useState(false);
  const [by, setBy] = useState(() => localStorage.getItem("raglit.author") ?? "");

  const load = useCallback(() => {
    let live = true;
    setError("");
    setUnread(false);
    getJSON<StateView>(base + "/state", { asset })
      .then((s) => live && setState(s))
      .catch((e: unknown) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 404) setUnread(true);
        else setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      live = false;
    };
  }, [base, asset]);

  useEffect(() => {
    setState(null);
    return load();
  }, [load]);

  const rule = async (unit: string, kind: string) => {
    if (!by.trim()) {
      setError("a ruling needs the name of the person making it");
      return;
    }
    localStorage.setItem("raglit.author", by.trim());
    try {
      await postJSON(base + "/verdict", undefined, { asset, unit, kind, by: by.trim() });
      load();
    } catch (e: unknown) {
      setError(String(e));
    }
  };

  if (unread) return <Unread index={index} asset={asset} onDone={load} />;
  if (error && !state) return <div className="empty err">{error}</div>;
  if (!state) return <div className="empty">loading…</div>;

  const s = state.stats ?? {};
  const total = s.total ?? 0;
  const ruled = total - (s.untouched ?? 0);
  const isAudio = /\.(mp3|wav|m4a|flac|ogg|opus|aac)$/i.test(asset);

  return (
    <>
      <nav className="crumb">
        <Link to="/i/$index" params={{ index }}>
          {index}
        </Link>
        <span className="sep">/</span>
        <Link to="/i/$index/attest" params={{ index }}>
          review
        </Link>
        <span className="sep">/</span>
        <span>{asset.split("/").pop()}</span>
      </nav>

      <div className="doctitle">
        <h2>{asset}</h2>
        <div className="bykind">
          <Progress ruled={ruled} total={total} />
          {/* Provenance is generated from the data and must be printed beside
              anything derived from this asset. A partly-reviewed transcript
              presented as a verified one launders an unchecked machine guess
              into the record. */}
          {state.producer && <span className="muted">read by {state.producer}</span>}
          {!!state.orphaned?.length && (
            <span className="badge error">{state.orphaned.length} orphaned verdicts</span>
          )}
        </div>
      </div>

      {isAudio && (
        <div className="prov">
          This is a recording. Playback and per-turn scrubbing live in the
          standalone workbench;{" "}
          <a href={`/attest/${encodeURIComponent(index)}?asset=${encodeURIComponent(asset)}`}>
            open it there
          </a>
          .
        </div>
      )}

      <div className="panel" style={{ marginTop: 12 }}>
        <div className="searchbar">
          {/* Deliberately not taken from the session: whoever holds the link may
              not be the account holder — an attorney hands a paralegal the link
              and the paralegal does the work — and the record has to say so. */}
          <input
            value={by}
            onChange={(e) => setBy(e.target.value)}
            placeholder="your name — a ruling is attributed to a person"
            autoComplete="name"
          />
          <span className="muted">
            {ruled}/{total} ruled
          </span>
        </div>

        {error && <div className="err" style={{ padding: "8px 14px" }}>{error}</div>}

        {(state.units ?? []).map((u) => (
          <div className="prob" key={u.unit.id}>
            <div className="meta" style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              {u.unit.label && <span className="badge">{u.unit.label}</span>}
              {u.kind && (
                <span className={"badge " + (u.kind === "confirmed" ? "done" : "")}>
                  {u.kind}
                  {u.swept ? " (swept)" : ""}
                </span>
              )}
              {u.unit.locator?.page != null && (
                <span className="muted">page {u.unit.locator.page}</span>
              )}
              {u.by && <span className="muted">by {u.by}</span>}
            </div>
            <div style={{ whiteSpace: "pre-wrap", margin: "4px 0" }}>
              {/* The correction wins when there is one: it is what the record
                  now says, and showing the machine's words as the current text
                  next to a "corrected" badge contradicts the badge. */}
              {u.text || u.unit.text || <span className="muted">(no text)</span>}
            </div>
            <div className="acts">
              {KINDS.map((k) => (
                <button
                  className="act"
                  key={k}
                  disabled={u.kind === k}
                  onClick={() => rule(u.unit.id, k)}
                >
                  {k}
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
    </>
  );
}

// Nothing has read this document, so there is nothing to rule on.
//
// Said plainly, with the thing that fixes it. The alternative — which is what
// this page did — was to render the mount's 404 body at the reader, who has no
// way to tell "your corpus has not been swept" from "the review server is
// broken", and every reason to assume the second.
function Unread({
  index,
  asset,
  onDone,
}: {
  index: string;
  asset: string;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<{ wrote: number; skipped: number } | null>(null);

  return (
    <div className="empty">
      <p>
        Nothing has read <b>{asset.split("/").pop()}</b> yet, so there is nothing
        to review. A review rules on what a MACHINE claimed; until something has
        claimed anything, there is no claim to confirm or dispute.
      </p>
      <p className="muted">
        A sweep writes a reading for every document in this index that can have
        one, and skips the rest — a spreadsheet, a zip, a PDF nobody has run
        regions over. It reports what it skipped rather than quietly covering
        half the corpus.
      </p>
      <button
        className="primary"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setErr("");
          try {
            const r = await postJSON<{ wrote: number; skipped: number }>(
              "/api/attest/readings",
              { index },
            );
            setResult(r);
            onDone();
          } catch (e: unknown) {
            setErr(e instanceof Error ? e.message : String(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? "sweeping…" : "Sweep this index"}
      </button>
      {result && (
        <p className="muted">
          wrote {result.wrote}, skipped {result.skipped}
          {result.skipped > 0 && " — a skipped document is one nothing can read"}
        </p>
      )}
      {err && <div className="err">{err}</div>}
    </div>
  );
}
