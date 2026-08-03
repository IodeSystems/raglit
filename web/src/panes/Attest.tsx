import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { getJSON } from "../api";

export type Stats = {
  total?: number;
  confirmed?: number;
  corrected?: number;
  affirmed?: number;
  unclear?: number;
  unsupported?: number;
  untouched?: number;
};

type AssetRef = {
  asset: string;
  kind?: string;
  producer?: string;
  stats?: Stats;
};

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
  const [assets, setAssets] = useState<AssetRef[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    setAssets(null);
    setError("");
    getJSON<{ assets: AssetRef[] }>(`/api/attest/${encodeURIComponent(index)}/assets`)
      .then((r) => live && setAssets(r.assets ?? []))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index]);

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
            <th className="num">Ruled</th>
            <th>Progress</th>
          </tr>
        </thead>
        <tbody>
          {assets.map((a) => {
            const s = a.stats ?? {};
            const total = s.total ?? 0;
            const ruled = total - (s.untouched ?? 0);
            return (
              <tr key={a.asset}>
                <td>
                  <Link to="/i/$index/attest/a/$asset" params={{ index, asset: a.asset }}>
                    {a.asset}
                  </Link>
                </td>
                <td className="muted">{a.producer || ""}</td>
                <td className="num">{total}</td>
                <td className="num">{ruled}</td>
                <td>
                  <Progress ruled={ruled} total={total} />
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
