import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { type Stats, ruled as ruledCount, useAssets } from "@iodesystems/attest-react";

import { raglitTransport } from "./attestTransport";
import { ApiError, postJSON } from "../api";
import { ErrorText } from "../ui/Details";
import { fileName } from "../ui/display";
import "./Attest.css";

export type { Stats };

// The review workbench's asset list, per index.
//
// The daemon already mounts one attest Service per index at
// /api/attest/<index> (cmd/raglit/attestmount.go) — one Service per index and
// not one for the daemon, because Service.Root is the security boundary and a
// single mount spanning every corpus would make one root out of unrelated trees.
// This page is just the client for the mount that already exists.
//
// The completeness numbers are why anyone opens this list. A list of names would
// send every reviewer into every asset to find the unfinished one, which is the
// thing Assets() resolves server-side to avoid.
export function Attest() {
  const { index } = useParams({ from: "/i/$index" });
  const transport = useMemo(() => raglitTransport(index), [index]);
  const { assets, error, reload } = useAssets(transport);
  // The list is ONE request, so there is nothing partial to draw until it
  // answers. What can be shown is that it is working, for how long, and — past
  // SLOW_AFTER seconds — a way out. That request once took 75 s (F2) and the
  // page said "loading…" the whole time, which reads as hung.
  const [attempt, setAttempt] = useState(0);
  const elapsed = useElapsed(assets === null && !error, `${index}#${attempt}`);
  // reload() clears the error itself, so the page drops straight back into the
  // loading view with the counter restarted.
  const retry = () => {
    setAttempt((n) => n + 1);
    reload();
  };

  if (error) {
    return (
      <div className="empty attest-state">
        <div className="attest-state-title">The review list could not be loaded.</div>
        <ErrorText
          raw={error}
          extra={
            <div>
              An index with no documents on disk has no folder to review against and
              is not set up for review.
            </div>
          }
        />
        <button onClick={retry}>Try again</button>
      </div>
    );
  }
  if (!assets) return <AssetsLoading elapsed={elapsed} onRetry={retry} />;
  if (!assets.length) {
    return (
      <div className="empty attest-state">
        <div className="attest-state-title">Nothing to review yet</div>
        <div>
          A document can be reviewed once the system has read it. Prepare the index to
          make every readable document available here.
        </div>
        <SweepIndex index={index} onDone={reload} />
      </div>
    );
  }

  return (
    <div className="panel table-scroll">
      <table className="attest-list">
        <thead>
          <tr>
            <th>Document</th>
            <th>Read by</th>
            <th className="num" title="Passages to review">Passages</th>
            {/*
              TWO COLUMNS, NOT ONE, AND THIS IS THE PORT'S FIRST CORRECTION.
              This table had a single "Ruled" column computed as total - untouched,
              which counts a blanket affirmation as an individual ruling and
              overstates how deeply the asset was reviewed. That is the exact
              distinction confirmed/affirmed exists to keep. `ruled()` from the core
              counts only what somebody went to individually; "accounted" is the
              other axis and both are needed to say what a pass was.
            */}
            <th className="num" title="Checked, corrected or disputed one at a time">Reviewed</th>
            <th>Progress</th>
          </tr>
        </thead>
        <tbody>
          {assets.map((a) => {
            const s = a.stats;
            const total = s?.total ?? 0;
            const accounted = total - (s?.untouched ?? 0);
            return (
              <tr key={a.asset}>
                <td>
                  <Link to="/i/$index/attest/a/$asset" params={{ index, asset: a.asset }}>
                    {fileName(a.asset)}
                  </Link>
                  {a.asset.includes("/") && (
                    <div className="muted attest-folder">{a.asset.slice(0, a.asset.lastIndexOf("/"))}</div>
                  )}
                </td>
                <td className="muted">{a.producer ?? ""}</td>
                <td className="num">{total}</td>
                <td className="num" title="Checked, corrected or disputed one at a time. A blanket approval is not counted here.">
                  {s ? ruledCount(s) : 0}
                </td>
                <td>
                  <Progress ruled={accounted} total={total} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// How long the list has been loading, in whole seconds; 0 when not loading.
// Restarts when the transport (the index) changes.
function useElapsed(loading: boolean, key: unknown): number {
  const [s, setS] = useState(0);
  useEffect(() => {
    setS(0);
    if (!loading) return;
    const t0 = Date.now();
    const id = window.setInterval(() => setS(Math.floor((Date.now() - t0) / 1000)), 1000);
    return () => window.clearInterval(id);
  }, [loading, key]);
  return s;
}

const SLOW_AFTER = 10;

// Skeleton rows in the table's own shape, so the page has its layout from the
// first frame, plus what is happening in words.
function AssetsLoading({ elapsed, onRetry }: { elapsed: number; onRetry: () => void }) {
  const slow = elapsed >= SLOW_AFTER;
  return (
    <div className="panel" aria-busy="true">
      <div className="attest-loading" role="status">
        <span className="attest-spinner" aria-hidden="true" />
        <span>
          Loading documents for review…
          {elapsed >= 2 && <span className="muted"> {elapsed}s</span>}
        </span>
        {slow && (
          <>
            <span className="muted">This is taking longer than usual.</span>
            <button onClick={onRetry}>Try again</button>
          </>
        )}
      </div>
      <table>
        <thead>
          <tr>
            <th>Document</th>
            <th>Read by</th>
            <th className="num">Passages</th>
            <th className="num">Reviewed</th>
            <th>Progress</th>
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: 6 }, (_, i) => (
            <tr key={i} className="attest-skel">
              <td><span style={{ width: `${55 + ((i * 17) % 35)}%` }} /></td>
              <td><span style={{ width: "60%" }} /></td>
              <td className="num"><span style={{ width: 28 }} /></td>
              <td className="num"><span style={{ width: 28 }} /></td>
              <td><span style={{ width: 90 }} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function Progress({ ruled, total }: { ruled: number; total: number }) {
  const pct = total ? Math.round((ruled * 100) / total) : 0;
  return (
    <span className="track" style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
      <span
        style={{
          display: "inline-block",
          width: 90,
          height: 6,
          borderRadius: 3,
          background: "var(--chip)",
          overflow: "hidden",
        }}
      >
        <span
          style={{
            display: "block",
            width: `${pct}%`,
            height: "100%",
            background: pct === 100 ? "var(--ok)" : "var(--run)",
          }}
        />
      </span>
      <span className="muted">{pct}%</span>
    </span>
  );
}

// Write a machine reading for every document in the index that can have one.
// A review rules on what a machine claimed, so this is what makes documents
// reviewable. It reports what it skipped (a spreadsheet, a zip, a PDF with no
// regions) rather than quietly covering half the index.
export function SweepIndex({ index, onDone }: { index: string; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<{ wrote: number; skipped: number } | null>(null);
  return (
    <div className="attest-sweep">
      <button
        className="primary"
        disabled={busy}
        title="Prepare every readable document in this index for review. Documents nothing can read are skipped and counted."
        onClick={async () => {
          setBusy(true);
          setErr("");
          try {
            const r = await postJSON<{ wrote: number; skipped: number }>("/api/attest/readings", {
              index,
            });
            setResult(r);
            onDone();
          } catch (e: unknown) {
            setErr(e instanceof ApiError ? e.message : String(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? "Preparing…" : "Prepare index for review"}
      </button>
      {result && (
        <div className="muted">
          {result.wrote.toLocaleString()} prepared, {result.skipped.toLocaleString()} skipped
          {result.skipped > 0 && " (a skipped document is one nothing can read)"}
        </div>
      )}
      {err && <ErrorText raw={err} />}
    </div>
  );
}
