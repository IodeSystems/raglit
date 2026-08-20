import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import {
  UNIT_VERDICTS,
  VERDICT_GLOSS,
  provenance,
  ruled as ruledCount,
  useAttestKeys,
  useWorkbench,
} from "@iodesystems/attest-react";

import { ApiError, postJSON } from "../api";
import { Progress } from "./Attest";
import { raglitTransport } from "./attestTransport";

// One asset under review: what the machine claimed, unit by unit, and what a
// person has ruled about each.
//
// PORTED ONTO @iodesystems/attest-react. What used to live here — the wire types,
// the verdict list, the load/rule pair — is the same code the standalone
// workbench and oidio each wrote separately, and it is now in one place. What
// stays here is what is genuinely raglit's: the CSS, the crumb, the sweep
// affordance, and the transport, which exists because this page has to tell a 404
// apart from a failure.
//
// AUDIO IS NOT HANDLED HERE. The standalone workbench (attest/ui.html) has a
// player built for scrubbing a two-hour hearing — gap-skipping, per-turn seek,
// Range-served media — and none of that is ported. An audio asset links out to
// that page rather than being shown here badly, because a review surface that
// silently cannot play the recording is worse than one that says so.
export function AttestAsset() {
  const { index, asset } = useParams({ from: "/i/$index/attest/a/$asset" });
  const transport = useMemo(() => raglitTransport(index), [index]);

  // Deliberately not taken from the session: whoever holds the link may not be
  // the account holder — an attorney hands a paralegal the link and the paralegal
  // does the work — and the record has to say so. The account travels separately,
  // supplied by the host, and both names land on every line.
  const [by, setBy] = useState(() => localStorage.getItem("raglit.author") ?? "");
  useEffect(() => {
    if (by.trim()) localStorage.setItem("raglit.author", by.trim());
  }, [by]);

  const wb = useWorkbench({ transport, assetId: asset, by: by.trim() });
  const [editing, setEditing] = useState(false);

  // The keyboard flow, with the guard that matters: a reviewer typing a
  // correction is not issuing commands, so `c` inside the field does not confirm
  // the unit and throw away what they were writing.
  useAttestKeys(wb, {
    enabled: Boolean(by.trim()),
    onEdit: () => setEditing(true),
    onSweep: () => setEditing(false),
  });

  // Distinguished from any other failure, because it is not one. attest lists and
  // serves assets WITH A MACHINE READING, so a 404 here means nothing has read
  // this document yet — a state with an action, not a broken mount.
  const unread = wb.error !== null && /\b404\b|not found/i.test(wb.error);
  if (unread) return <Unread index={index} asset={asset} onDone={wb.reload} />;
  if (wb.error && !wb.state) return <div className="empty err">{wb.error}</div>;
  if (!wb.state) return <div className="empty">loading…</div>;

  const s = wb.state.stats;
  const total = s.total;
  const accounted = total - s.untouched;
  const prov = provenance(s);
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
          <Progress ruled={accounted} total={total} />
          {wb.state.producer && <span className="muted">read by {wb.state.producer}</span>}
          {!!wb.state.orphaned?.length && (
            <span className="badge error" title="a re-read changed what the machine claims; these verdicts no longer attach to anything, and are never matched onto whatever is nearest">
              {wb.state.orphaned.length} orphaned verdicts
            </span>
          )}
        </div>
      </div>

      {/*
        Provenance is generated from the data and must be printed beside anything
        derived from this asset. A partly-reviewed transcript presented as a
        verified one launders an unchecked machine guess into the record.
        TWO AXES, never one number: whether the claims were accounted for, and
        whether the words were corrected. The terms of an affirmation are QUOTED,
        because "reasonably certain there are only minor errors" is a different
        assertion from "the rest is right".
      */}
      <div className={prov.complete ? "prov" : "prov warn"}>
        {prov.accounted} · {prov.corrections}
        {prov.terms && <div className="muted">affirmed under: “{prov.terms}”</div>}
        {prov.termsMissing && (
          <div className="muted">
            the affirmation’s terms were not recorded — that is what is known, and
            nothing here will invent them
          </div>
        )}
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
          <input
            value={by}
            onChange={(e) => setBy(e.target.value)}
            placeholder="your name — a ruling is attributed to a person"
            autoComplete="name"
          />
          <span className="muted" title="checked, corrected or disputed one at a time; a sweep is not counted here">
            {ruledCount(s)} ruled · {accounted}/{total} accounted for
          </span>
          <SweepButton disabled={!by.trim()} onSweep={(terms) => void wb.sweep(terms)} />
        </div>

        {!by.trim() && (
          <div className="muted" style={{ padding: "8px 14px" }}>
            A ruling needs the name of the person making it. Self-declared, and
            required: a defaulted author reads afterwards exactly like a real one.
          </div>
        )}
        {wb.error && <div className="err" style={{ padding: "8px 14px" }}>{wb.error}</div>}

        {wb.state.units.map((u, i) => {
          const active = i === wb.cursor;
          const pending = wb.pending.get(u.unit.id);
          return (
            <div
              className={"prob" + (active ? " active" : "")}
              key={u.unit.id}
              onClick={() => wb.goto(i)}
            >
              <div className="meta" style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
                {(u.label || u.unit.label) && <span className="badge">{u.label || u.unit.label}</span>}
                {u.kind && (
                  <span className={"badge " + (u.kind === "confirmed" ? "done" : "")}>
                    {u.kind}
                    {/* Swept is NOT a demotion — the reviewer went through it, and
                        the affirmation they signed says under what terms. It is
                        marked so a reader can ask whether anyone named this one. */}
                    {u.swept ? " (swept)" : ""}
                  </span>
                )}
                {u.unit.locator.area?.page != null && (
                  <span className="muted">page {u.unit.locator.area.page}</span>
                )}
                {u.by && <span className="muted">by {u.by}</span>}
                {u.authored && <span className="muted" title="a unit a person cut themselves">authored</span>}
              </div>

              {active && editing ? (
                <textarea
                  autoFocus
                  className="edit"
                  style={{ width: "100%", minHeight: 72, margin: "4px 0" }}
                  defaultValue={u.text || u.unit.text || ""}
                  onChange={(e) => wb.edit({ text: e.target.value })}
                  onBlur={() => {
                    setEditing(false);
                    void wb.flush();
                  }}
                />
              ) : (
                <div style={{ whiteSpace: "pre-wrap", margin: "4px 0" }}>
                  {/* The correction wins when there is one: it is what the record
                      now says, and showing the machine's words as the current text
                      next to a "corrected" badge contradicts the badge. */}
                  {pending?.text ?? u.text ?? u.unit.text ?? <span className="muted">(no text)</span>}
                  {pending && <span className="muted"> · unsaved</span>}
                </div>
              )}

              <div className="acts">
                {UNIT_VERDICTS.map((k) => (
                  <button
                    className="act"
                    key={k}
                    title={VERDICT_GLOSS[k]}
                    disabled={u.kind === k || !by.trim()}
                    onClick={(e) => {
                      e.stopPropagation();
                      wb.goto(i);
                      void wb.rule(k);
                    }}
                  >
                    {k}
                  </button>
                ))}
                {u.kind && (
                  <button
                    className="act"
                    title="take back the ruling; the unit returns to untouched"
                    onClick={(e) => {
                      e.stopPropagation();
                      wb.goto(i);
                      void wb.retract();
                    }}
                  >
                    retract
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </>
  );
}

// A BLANKET AFFIRMATION ASKS FOR ITS TERMS, ALWAYS.
//
// It is a qualified claim with a materiality standard inside it, and a button
// that swept without asking would record one the reviewer never made. The prompt
// is theirs to fill or leave empty; an empty one is recorded as empty and says so
// downstream rather than being filled in with "the rest is right".
function SweepButton({ disabled, onSweep }: { disabled: boolean; onSweep: (terms?: string) => void }) {
  const [open, setOpen] = useState(false);
  const [terms, setTerms] = useState("");
  if (!open) {
    return (
      <button className="act" disabled={disabled} onClick={() => setOpen(true)}>
        affirm the rest…
      </button>
    );
  }
  return (
    <span style={{ display: "inline-flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
      <input
        autoFocus
        value={terms}
        onChange={(e) => setTerms(e.target.value)}
        placeholder="the terms you are affirming under, in your own words"
        style={{ minWidth: 320 }}
      />
      <button
        className="primary"
        onClick={() => {
          onSweep(terms.trim() || undefined);
          setOpen(false);
          setTerms("");
        }}
      >
        affirm
      </button>
      <button className="act" onClick={() => setOpen(false)}>
        cancel
      </button>
    </span>
  );
}

// Nothing has read this document, so there is nothing to rule on.
//
// Said plainly, with the thing that fixes it. The alternative — which is what
// this page did — was to render the mount's 404 body at the reader, who has no
// way to tell "your corpus has not been swept" from "the review server is
// broken", and every reason to assume the second.
function Unread({ index, asset, onDone }: { index: string; asset: string; onDone: () => void }) {
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
            setErr(e instanceof ApiError ? e.message : String(e));
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
