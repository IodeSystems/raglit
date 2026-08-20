import { Link, useParams } from "@tanstack/react-router";
import { useMemo } from "react";
import { type Stats, ruled as ruledCount, useAssets } from "@iodesystems/attest-react";

import { raglitTransport } from "./attestTransport";

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
  const { assets, error } = useAssets(transport);

  if (error) {
    return (
      <div className="empty err">
        {error}
        <div className="muted" style={{ marginTop: 8 }}>
          An index with no documents on disk has no root to bound a review to and
          is not mounted at all.
        </div>
      </div>
    );
  }
  if (!assets) return <div className="empty">loading…</div>;
  if (!assets.length) {
    return (
      <div className="empty">
        Nothing has a machine reading yet, so there is nothing to review. A sweep
        (<code>POST /api/attest/readings</code>) is what turns indexed documents
        into reviewable ones.
      </div>
    );
  }

  return (
    <div className="panel">
      <table>
        <thead>
          <tr>
            <th>Asset</th>
            <th>Producer</th>
            <th className="num">Units</th>
            {/*
              TWO COLUMNS, NOT ONE, AND THIS IS THE PORT'S FIRST CORRECTION.
              This table had a single "Ruled" column computed as total - untouched,
              which counts a blanket affirmation as an individual ruling and
              overstates how deeply the asset was reviewed. That is the exact
              distinction confirmed/affirmed exists to keep. `ruled()` from the core
              counts only what somebody went to individually; "accounted" is the
              other axis and both are needed to say what a pass was.
            */}
            <th className="num">Ruled</th>
            <th>Accounted for</th>
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
                    {a.asset}
                  </Link>
                </td>
                <td className="muted">{a.producer ?? ""}</td>
                <td className="num">{total}</td>
                <td className="num" title="checked, corrected or disputed one at a time — a sweep is not counted here">
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
