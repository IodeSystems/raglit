import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";

import { listDocuments, type DocumentRow } from "../api";
import { aiTitleTooltip, displayPath, displayTitle, documents, fileName, kindLabel, passages, scannedPages } from "../ui/display";
import { ErrorText } from "../ui/Details";
import { DocTree } from "./DocTree";
import "./DocumentList.css";
import { useOperator } from "../ui/operator";

// Every document in the index.
//
// The text filter, kind and sort are local state: a way to find a row on this
// screen, not a thing worth sending. The facet selection (document type, tag,
// coverage mark) is the exception — it is in the URL's search params, so a
// filtered list is a link.

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
  const name = displayTitle(d.name || d.gen_name || "");
  const title = name || displayTitle(d.title || "") || fileName(rel);
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
    hay: `${title}\n${rel}\n${kind}\n${kindLabel(kind)}`.toLowerCase(),
  };
}

const dateFmt = new Intl.DateTimeFormat(undefined, { year: "numeric", month: "short", day: "numeric" });

// The five coverage marks, in the order they are read: captioned, tagged,
// typed, extracted, attested. Only the ones set are shown; an all-fresh
// document shows none.
type CoverageKey = "captioned" | "tagged" | "typed" | "extracted" | "attested";
const COVERAGE_MARKS: { key: CoverageKey; label: string }[] = [
  { key: "captioned", label: "captioned" },
  { key: "tagged", label: "tagged" },
  { key: "typed", label: "typed" },
  { key: "extracted", label: "extracted" },
  { key: "attested", label: "attested" },
];

// A row shows at most this many tags of each kind; the rest collapse into +N
// with the full list in the title, so a heavily tagged document does not take
// over the row.
const MAX_TAGS = 3;

function TagChips({ label, tags }: { label: string; tags: string[] }) {
  if (!tags.length) return null;
  const shown = tags.slice(0, MAX_TAGS);
  const rest = tags.length - shown.length;
  return (
    <>
      {shown.map((t) => (
        <span key={t} className="badge dl-tag" title={`${label} tag`}>
          {t}
        </span>
      ))}
      {rest > 0 && (
        <span className="badge dl-tag muted" title={tags.join(", ")}>
          +{rest}
        </span>
      )}
    </>
  );
}

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
      // Titled documents first: an untitled one shows its file name or the
      // first line OCR read, which is a poor first screen.
      if (a.named !== b.named) return a.named ? -1 : 1;
      return byTitle;
  }
}

export function DocumentList() {
  const { index } = useParams({ from: "/i/$index" });
  const operator = useOperator();
  // Facet selection lives in the URL: ?type=, ?tag= and ?cov= are set by
  // clicking a facet chip, and cleared by clicking it again. One value per
  // facet at a time — a facet row is a choice, and ANDing within a facet has
  // no meaning (a document carries one doc type).
  const { type: typeSel, tag: tagSel, cov: covSel, cov_missing: covMissingSel } = useSearch({ from: "/i/$index/d" });
  const navigate = useNavigate();
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState("");
  const [kind, setKind] = useState<string | null>(null); // null = all kinds
  const [sort, setSort] = useState<SortKey>("title");
  const [view, setView] = useState<"list" | "tree">("list");
  const [root, setRoot] = useState<string | undefined>();
  const listRef = useRef<HTMLDivElement>(null);
  const filterRef = useRef<HTMLInputElement>(null);

  // Every facet change replaces: the row is a control, and a shared link is
  // one URL, not a trail of toggles. `to` is the current route, which also
  // pins the search type to this route's params.
  function setFacet(key: "type" | "tag" | "cov", value: string | null) {
    void navigate({
      to: "/i/$index/d",
      params: { index },
      search: (s) => ({ ...s, [key]: value ? value : undefined }),
      replace: true,
    });
  }

  function clearFacets() {
    void navigate({
      to: "/i/$index/d",
      params: { index },
      search: (s) => ({ ...s, type: undefined, tag: undefined, cov: undefined, cov_missing: undefined }),
      replace: true,
    });
  }

  useEffect(() => {
    let live = true;
    listDocuments(index)
      .then((r) => {
        if (!live) return;
        setRoot(r.root);
        setRows((r.documents ?? []).map((d) => toRow(d, r.root)));
      })
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

  // The three facet rows. Counts come from the text-filtered rows, so a chip's
  // number is what clicking it would show. Doc type is what identity resolved
  // the document as; tags are the content and role tags together (tag values
  // are unique across the two); coverage marks are the five presence marks.
  // A tag index with hundreds of values would not fit the screen, so the row
  // shows the most common ones.
  const MAX_TAGS_FACET = 12;
  const facets = useMemo(() => {
    if (!rows) return { types: [] as [string, number][], tags: [] as [string, number][], covs: [] as [string, number][] };
    const typeCounts = new Map<string, number>();
    const tagCounts = new Map<string, number>();
    const covCounts = new Map<string, number>();
    for (const r of matched ?? []) {
      // The type facet is a choice among RESOLVED types; "no type" is not a
      // type, so it gets no chip (unlike the kind row, where unclassified is a
      // state worth filtering to).
      const t = r.doc.gen_doc_type;
      if (t) typeCounts.set(t, (typeCounts.get(t) ?? 0) + 1);
      for (const tag of [...(r.doc.gen_content_tags ?? []), ...(r.doc.gen_role_tags ?? [])]) {
        tagCounts.set(tag, (tagCounts.get(tag) ?? 0) + 1);
      }
      for (const m of COVERAGE_MARKS) {
        if (r.doc.coverage?.[m.key]) covCounts.set(m.key, (covCounts.get(m.key) ?? 0) + 1);
      }
    }
    const byCount = (a: [string, number], b: [string, number]) => b[1] - a[1] || a[0].localeCompare(b[0]);
    const types = [...typeCounts.entries()].sort(byCount);
    const tags = [...tagCounts.entries()].sort(byCount).slice(0, MAX_TAGS_FACET);
    const covs = COVERAGE_MARKS.map((m) => [m.key, covCounts.get(m.key) ?? 0] as [string, number]).filter(
      ([, n]) => n > 0,
    );
    return { types, tags, covs };
  }, [matched]);

  const shown = useMemo(() => {
    if (!matched) return null;
    const out = matched.filter(
      (r) =>
        (kind === null || r.kind === kind) &&
        (!typeSel || (r.doc.gen_doc_type || "") === typeSel) &&
        (!tagSel ||
          (r.doc.gen_content_tags ?? []).includes(tagSel) ||
          (r.doc.gen_role_tags ?? []).includes(tagSel)) &&
        (!covSel || !!r.doc.coverage?.[covSel as CoverageKey]) &&
        (!covMissingSel || !r.doc.coverage?.[covMissingSel as CoverageKey]),
    );
    return out.sort((a, b) => compare(a, b, sort));
  }, [matched, kind, sort, typeSel, tagSel, covSel, covMissingSel]);

  const anyFacet = !!(typeSel || tagSel || covSel || covMissingSel);

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
        <div className="dl-view" role="group" aria-label="View">
          {(["list", "tree"] as const).map((v) => (
            <button
              key={v}
              type="button"
              className={view === v ? "on" : ""}
              aria-pressed={view === v}
              onClick={() => setView(v)}
            >
              {v === "list" ? "List" : "Tree"}
            </button>
          ))}
        </div>
        <label className="dl-sort" hidden={view === "tree"}>
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
              {k ? kindLabel(k) : "Not classified"} <span className="dl-count">{n.toLocaleString()}</span>
            </button>
          ))}
        </div>
      )}

      {/* The facet rows: document type, tag and coverage mark. The selection
          is in the URL (?type= ?tag= ?cov=), so this row is what a shared link
          restores. A row is hidden until at least one of its values is
          present, so a fresh index shows only the kind row. */}
      {(facets.types.length > 0 || facets.tags.length > 0 || facets.covs.length > 0 || covMissingSel) && (
        <div className="dl-facets">
          {anyFacet && (
            <button type="button" className="dl-clear" onClick={clearFacets}>
              Clear
            </button>
          )}
          {facets.types.length > 0 && (
            <div className="dl-facet">
              <span className="muted dl-facet-label">Type</span>
              {typeSel && (
                <button type="button" className="dl-chip on" aria-pressed onClick={() => setFacet("type", null)}>
                  {typeSel} <span className="dl-count">×</span>
                </button>
              )}
              {facets.types.map(([t, n]) => (
                <button type="button" key={t} className="dl-chip" onClick={() => setFacet("type", t)}>
                  {t} <span className="dl-count">{n.toLocaleString()}</span>
                </button>
              ))}
            </div>
          )}
          {facets.tags.length > 0 && (
            <div className="dl-facet">
              <span className="muted dl-facet-label">Tag</span>
              {tagSel && (
                <button type="button" className="dl-chip on" aria-pressed onClick={() => setFacet("tag", null)}>
                  {tagSel} <span className="dl-count">×</span>
                </button>
              )}
              {facets.tags.map(([t, n]) => (
                <button type="button" key={t} className="dl-chip" onClick={() => setFacet("tag", t)}>
                  {t} <span className="dl-count">{n.toLocaleString()}</span>
                </button>
              ))}
            </div>
          )}
          {facets.covs.length > 0 && (
            <div className="dl-facet">
              <span className="muted dl-facet-label">Coverage</span>
              {covSel && (
                <button type="button" className="dl-chip on" aria-pressed onClick={() => setFacet("cov", null)}>
                  {covSel} <span className="dl-count">×</span>
                </button>
              )}
              {covMissingSel && (
                <button
                  type="button"
                  className="dl-chip on"
                  aria-pressed
                  onClick={() =>
                    void navigate({
                      to: "/i/$index/d",
                      params: { index },
                      search: (s) => ({ ...s, cov_missing: undefined }),
                      replace: true,
                    })
                  }
                >
                  missing {covMissingSel} <span className="dl-count">×</span>
                </button>
              )}
              {facets.covs.map(([c, n]) => (
                <button type="button" key={c} className="dl-chip" onClick={() => setFacet("cov", c)}>
                  {c} <span className="dl-count">{n.toLocaleString()}</span>
                </button>
              ))}
            </div>
          )}
          {/* Show the cov_missing filter even when no coverage marks exist yet
              (a fresh index): the user landed here from a dashboard shortfall
              link and needs to see what is being filtered. */}
          {covMissingSel && facets.covs.length === 0 && (
            <div className="dl-facet">
              <span className="muted dl-facet-label">Coverage</span>
              <button
                type="button"
                className="dl-chip on"
                aria-pressed
                onClick={() =>
                  void navigate({
                    to: "/i/$index/d",
                    params: { index },
                    search: (s) => ({ ...s, cov_missing: undefined }),
                    replace: true,
                  })
                }
              >
                missing {covMissingSel} <span className="dl-count">×</span>
              </button>
            </div>
          )}
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

      {!!shown?.length && view === "tree" && (
        <DocTree
          index={index}
          root={root}
          docs={shown.map((r) => ({ doc: r.doc, rel: r.rel, title: r.title, kind: r.kind }))}
        />
      )}
      {!!shown?.length && view === "list" && (
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
                  {/* Not twice: with no better title the file name IS the
                      title, and a second identical line read as a layout
                      mistake (focus groups, 2026-10-24). */}
                  {r.title !== r.rel && <div className="dl-path">{r.rel}</div>}
                </div>
                <div className="dl-meta">
                  {r.kind && <span className="badge dl-kind">{kindLabel(r.kind)}</span>}
                  {r.doc.gen_doc_type && (
                    <span className="badge dl-doctype" title="Resolved document type">
                      {r.doc.gen_doc_type}
                    </span>
                  )}
                  <TagChips label="content" tags={r.doc.gen_content_tags ?? []} />
                  <TagChips label="role" tags={r.doc.gen_role_tags ?? []} />
                  {COVERAGE_MARKS.filter((m) => r.doc.coverage?.[m.key]).map((m) => (
                    <span key={m.key} className="badge dl-cov" title={`Coverage: ${m.label}`}>
                      {m.label}
                    </span>
                  ))}
                  {!!r.doc.vision && <span className="badge vision">{scannedPages(r.doc.vision)}</span>}
                  {operator && <span className="muted dl-num">{passages(r.doc.fragments ?? 0)}</span>}
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
