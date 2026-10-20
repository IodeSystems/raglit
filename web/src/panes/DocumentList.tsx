import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";

import { listDocuments, type DocumentRow } from "../api";
import { aiTitleTooltip, displayPath, documents, fileName, passages, scannedPages } from "../ui/display";
import { ErrorText } from "../ui/Details";
import "./DocumentList.css";

// Every document in the index.
//
// The filter, kind and sort are local state, not URL params, and that is
// deliberate: they are a way to find a row on this screen, not a thing worth
// sending anybody. The search route is what you send.

type SortKey = "title" | "added" | "kind";

const SORTS: { value: SortKey; label: string }[] = [
  { value: "title", label: "Title" },
  { value: "added", label: "Date added" },
  { value: "kind", label: "Kind" },
];

// The kind filter's value for documents nobody has classified yet.
const NO_KIND = "";

type Row = {
  doc: DocumentRow;
  rel: string; // display path, relative to the index root
  title: string; // what the document IS, or its file name when nobody has said
  named: boolean; // title came from identity, not the file name
  kind: string;
  added?: Date;
  hay: string; // lower-cased text the filter matches against
};

function toRow(d: DocumentRow, root?: string): Row {
  const rel = d.rel || displayPath(d.path, root);
  const name = d.name || d.gen_name || "";
  const title = name || d.title || fileName(rel);
  const kind = d.kind || d.gen_kind || NO_KIND;
  // added_at is unix NANOseconds; a Date takes milliseconds.
  const added = d.added_at ? new Date(d.added_at / 1e6) : undefined;
  return {
    doc: d,
    rel,
    title,
    named: !!name,
    kind,
    added,
    hay: `${title}\n${rel}\n${kind}`.toLowerCase(),
  };
}

const dateFmt = new Intl.DateTimeFormat(undefined, { year: "numeric", month: "short", day: "numeric" });

function compare(a: Row, b: Row, key: SortKey): number {
  const byTitle = a.title.localeCompare(b.title, undefined, { sensitivity: "base", numeric: true });
  switch (key) {
    case "added": {
      // Newest first; undated rows last.
      const d = (b.added?.getTime() ?? -Infinity) - (a.added?.getTime() ?? -Infinity);
      return d || byTitle;
    }
    case "kind": {
      // Unclassified rows after every named kind.
      if (a.kind !== b.kind) {
        if (a.kind === NO_KIND) return 1;
        if (b.kind === NO_KIND) return -1;
        return a.kind.localeCompare(b.kind);
      }
      return byTitle;
    }
    default:
      return byTitle;
  }
}

export function DocumentList() {
  const { index } = useParams({ from: "/i/$index" });
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState("");
  const [kind, setKind] = useState<string | null>(null); // null = all kinds
  const [sort, setSort] = useState<SortKey>("title");
  const listRef = useRef<HTMLDivElement>(null);
  const filterRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let live = true;
    setRows(null);
    setError("");
    setKind(null);
    listDocuments(index)
      .then((r) => live && setRows((r.documents ?? []).map((d) => toRow(d, r.root))))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index]);

  // Rows that pass the text filter. Kind counts are taken from these, so a
  // chip's number is what clicking it would show.
  const matched = useMemo(() => {
    if (!rows) return null;
    const f = filter.trim().toLowerCase();
    if (!f) return rows;
    const words = f.split(/\s+/);
    return rows.filter((r) => words.every((w) => r.hay.includes(w)));
  }, [rows, filter]);

  const kinds = useMemo(() => {
    if (!rows) return [];
    const counts = new Map<string, number>();
    // Every kind in the index gets a chip, even at zero, so the chips do not
    // jump around while the filter is typed.
    for (const r of rows) counts.set(r.kind, 0);
    for (const r of matched ?? []) counts.set(r.kind, (counts.get(r.kind) ?? 0) + 1);
    return [...counts.entries()].sort((a, b) => {
      if (a[0] === NO_KIND) return 1;
      if (b[0] === NO_KIND) return -1;
      return b[1] - a[1] || a[0].localeCompare(b[0]);
    });
  }, [rows, matched]);

  const shown = useMemo(() => {
    if (!matched) return null;
    const out = kind === null ? [...matched] : matched.filter((r) => r.kind === kind);
    return out.sort((a, b) => compare(a, b, sort));
  }, [matched, kind, sort]);

  // Arrow keys move between rows; Home/End jump; "/" goes back to the filter.
  function onListKey(e: KeyboardEvent<HTMLDivElement>) {
    const links = Array.from(listRef.current?.querySelectorAll<HTMLAnchorElement>("a.dl-row") ?? []);
    if (!links.length) return;
    const at = links.indexOf(document.activeElement as HTMLAnchorElement);
    let next = -1;
    if (e.key === "ArrowDown") next = Math.min(at + 1, links.length - 1);
    else if (e.key === "ArrowUp") {
      if (at <= 0) {
        e.preventDefault();
        filterRef.current?.focus();
        return;
      }
      next = at - 1;
    } else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = links.length - 1;
    else if (e.key === "/") {
      e.preventDefault();
      filterRef.current?.focus();
      return;
    }
    if (next < 0) return;
    e.preventDefault();
    links[next]?.focus();
  }

  function onFilterKey(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      listRef.current?.querySelector<HTMLAnchorElement>("a.dl-row")?.focus();
    } else if (e.key === "Escape" && filter) {
      e.preventDefault();
      setFilter("");
    }
  }

  const total = rows?.length ?? 0;

  return (
    <div className="panel doclist">
      <div className="searchbar dl-bar">
        <input
          ref={filterRef}
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          onKeyDown={onFilterKey}
          placeholder="Filter by title, file or kind"
          aria-label="Filter documents"
          autoComplete="off"
        />
        <label className="dl-sort">
          <span className="muted">Sort</span>
          <select value={sort} onChange={(e) => setSort(e.target.value as SortKey)}>
            {SORTS.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </select>
        </label>
      </div>

      {kinds.length > 1 && (
        <div className="dl-kinds" role="group" aria-label="Filter by kind">
          <button
            type="button"
            className={"dl-chip" + (kind === null ? " on" : "")}
            aria-pressed={kind === null}
            onClick={() => setKind(null)}
          >
            All <span className="dl-count">{(matched?.length ?? 0).toLocaleString()}</span>
          </button>
          {kinds.map(([k, n]) => (
            <button
              type="button"
              key={k || "(none)"}
              className={"dl-chip" + (kind === k ? " on" : "") + (k === NO_KIND ? " none" : "")}
              aria-pressed={kind === k}
              disabled={n === 0 && kind !== k}
              onClick={() => setKind(kind === k ? null : k)}
            >
              {k || "Not classified"} <span className="dl-count">{n.toLocaleString()}</span>
            </button>
          ))}
        </div>
      )}

      {error && (
        <div className="empty">
          <p>Could not load the document list.</p>
          <ErrorText raw={error} />
        </div>
      )}
      {!error && shown === null && <div className="empty">Loading documents…</div>}
      {shown !== null && total === 0 && <div className="empty">This index has no documents yet.</div>}
      {shown?.length === 0 && total > 0 && <div className="empty">No documents match.</div>}

      {!!shown?.length && (
        <>
          <div className="dl-summary muted" aria-live="polite">
            {shown.length === total ? documents(total) : `${shown.length.toLocaleString()} of ${documents(total)}`}
          </div>
          <div className="dl-list" ref={listRef} onKeyDown={onListKey}>
            {shown.map((r) => (
              <Link
                className="dl-row"
                key={r.doc.path}
                to="/i/$index/d/$doc/pages"
                params={{ index, doc: r.rel }}
              >
                <div className="dl-main">
                  {/* What a person or a machine said it IS, above where the
                      file lives. The path stays visible because the title is
                      the thing most likely to be wrong. */}
                  <div
                    className="dl-title"
                    title={r.named && r.doc.gen_source !== "person" ? aiTitleTooltip(r.doc.gen_model) : undefined}
                  >
                    {r.title}
                  </div>
                  <div className="dl-path">{r.rel}</div>
                </div>
                <div className="dl-meta">
                  {r.kind && <span className="badge dl-kind">{r.kind}</span>}
                  {!!r.doc.vision && <span className="badge vision">{scannedPages(r.doc.vision)}</span>}
                  <span className="muted dl-num">{passages(r.doc.fragments ?? 0)}</span>
                  {r.added && (
                    <time className="muted dl-date" dateTime={r.added.toISOString()} title="Date added">
                      {dateFmt.format(r.added)}
                    </time>
                  )}
                </div>
              </Link>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
