import { Link } from "@tanstack/react-router";
import { useMemo, useState } from "react";

import type { DocumentRow } from "../api";
import { documents, fileName, kindLabel } from "../ui/display";

// The index as folders, each rolled up (USER 2026-10-22, plan/index-tree.md):
// how many documents, pages and bytes are under it, the file types and the
// kinds, and the span of the files' modified dates. It answers "what is in
// here and what period does it cover" without opening anything — and shows a
// node_modules at a glance, with a link to purge it.

export type TreeDoc = { doc: DocumentRow; rel: string; title: string; kind: string };

type Rollup = {
  docs: number;
  pages: number;
  bytes: number;
  sized: number; // documents whose size is known
  earliest: number; // unix seconds, 0 = none known
  latest: number;
  exts: Map<string, number>;
  kinds: Map<string, number>;
};

type Node = {
  name: string;
  path: string; // display path of the folder, relative to the index root
  dirs: Map<string, Node>;
  files: TreeDoc[];
  sum: Rollup;
};

const emptyRollup = (): Rollup => ({
  docs: 0,
  pages: 0,
  bytes: 0,
  sized: 0,
  earliest: 0,
  latest: 0,
  exts: new Map(),
  kinds: new Map(),
});

function ext(rel: string): string {
  const base = fileName(rel);
  const dot = base.lastIndexOf(".");
  return dot > 0 ? base.slice(dot + 1).toLowerCase() : "(none)";
}

function add(sum: Rollup, d: TreeDoc) {
  sum.docs++;
  sum.pages += d.doc.pages ?? 0;
  const size = d.doc.src_size ?? 0;
  if (size > 0) {
    sum.bytes += size;
    sum.sized++;
  }
  const m = d.doc.src_mtime ?? 0;
  if (m > 0) {
    if (!sum.earliest || m < sum.earliest) sum.earliest = m;
    if (m > sum.latest) sum.latest = m;
  }
  const e = ext(d.rel);
  sum.exts.set(e, (sum.exts.get(e) ?? 0) + 1);
  sum.kinds.set(d.kind, (sum.kinds.get(d.kind) ?? 0) + 1);
}

function build(docs: TreeDoc[]): Node {
  const root: Node = { name: "", path: "", dirs: new Map(), files: [], sum: emptyRollup() };
  for (const d of docs) {
    const parts = d.rel.split("/").filter(Boolean);
    // A document outside the index root keeps its absolute path; its first
    // folder keeps the leading "/" so the purge pattern stays absolute too.
    if (d.rel.startsWith("/") && parts.length) parts[0] = "/" + parts[0];
    let at = root;
    add(at.sum, d);
    for (const part of parts.slice(0, -1)) {
      let next = at.dirs.get(part);
      if (!next) {
        next = {
          name: part,
          path: at.path ? `${at.path}/${part}` : part,
          dirs: new Map(),
          files: [],
          sum: emptyRollup(),
        };
        at.dirs.set(part, next);
      }
      at = next;
      add(at.sum, d);
    }
    at.files.push(d);
  }
  return root;
}

// A folder with one subfolder and no files of its own says nothing on its own
// row; joined with its child, a deep tree opens at the first folder that holds
// a choice.
function collapse(n: Node): Node {
  let at = n;
  for (;;) {
    const only = at.files.length === 0 && at.dirs.size === 1 ? [...at.dirs.values()][0] : undefined;
    if (!only) return at;
    at = { ...only, name: `${at.name}/${only.name}` };
  }
}

const dateFmt = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
});
const day = (s: number) => dateFmt.format(new Date(s * 1000));

function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

function top(m: Map<string, number>, n: number): [string, number][] {
  return [...m.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).slice(0, n);
}

function span(sum: Rollup): string {
  if (!sum.earliest) return "";
  const a = day(sum.earliest);
  const b = day(sum.latest);
  return a === b ? a : `${a} – ${b}`;
}

function Stats({ sum, folder }: { sum: Rollup; folder: boolean }) {
  return (
    <div className="dt-stats">
      {folder && <span className="dt-num">{documents(sum.docs)}</span>}
      {sum.pages > 0 && <span className="dt-num muted">{sum.pages.toLocaleString()} p</span>}
      {sum.sized > 0 && (
        <span
          className="dt-num muted"
          title={
            sum.sized < sum.docs ? `${sum.docs - sum.sized} document(s) of unknown size` : undefined
          }
        >
          {bytes(sum.bytes)}
          {sum.sized < sum.docs ? "+" : ""}
        </span>
      )}
      {sum.earliest > 0 && (
        <span className="dt-date muted" title="File modified dates">
          {span(sum)}
        </span>
      )}
    </div>
  );
}

function Chips({ sum }: { sum: Rollup }) {
  const exts = top(sum.exts, 4);
  const kinds = top(sum.kinds, 3).filter(([k]) => k);
  return (
    <div className="dt-chips">
      {exts.map(([e, n]) => (
        <span key={"e" + e} className="dt-chip" title={`${n} .${e} file(s)`}>
          {e} <span className="dl-count">{n.toLocaleString()}</span>
        </span>
      ))}
      {sum.exts.size > exts.length && (
        <span className="dt-chip muted">+{sum.exts.size - exts.length}</span>
      )}
      {kinds.map(([k, n]) => (
        <span key={"k" + k} className="dt-chip kind" title={`${n} classified as ${kindLabel(k)}`}>
          {kindLabel(k)} <span className="dl-count">{n.toLocaleString()}</span>
        </span>
      ))}
    </div>
  );
}

export function DocTree({ index, root, docs }: { index: string; root?: string; docs: TreeDoc[] }) {
  const tree = useMemo(() => build(docs), [docs]);
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const toggle = (p: string) =>
    setOpen((cur) => {
      const next = new Set(cur);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });

  // A folder's purge pattern: the absolute directory, so it matches exactly
  // this folder and not a same-named one elsewhere. Only for corpus paths;
  // raglit's own storage ("@…") has no root to anchor to.
  const purgeGlob = (folder: string) => {
    if (folder.startsWith("/")) return `${folder}/**`;
    return root && !folder.startsWith("@") ? `${root}/${folder}/**` : "";
  };

  const renderNode = (raw: Node, depth: number): React.ReactNode => {
    const n = collapse(raw);
    const isOpen = open.has(n.path);
    const glob = purgeGlob(n.path);
    return (
      <div key={"d:" + n.path} role="treeitem" aria-expanded={isOpen}>
        <div className="dt-row dt-dir" style={{ paddingLeft: 14 + depth * 18 }}>
          <button
            type="button"
            className="dt-toggle"
            onClick={() => toggle(n.path)}
            aria-label={isOpen ? "Collapse" : "Expand"}
          >
            {isOpen ? "▾" : "▸"}
          </button>
          <button type="button" className="dt-name" onClick={() => toggle(n.path)}>
            {n.name}/
          </button>
          <Chips sum={n.sum} />
          <Stats sum={n.sum} folder />
          {glob && (
            <Link
              className="dt-purge muted"
              to="/i/$index/controls"
              params={{ index }}
              search={{ purge: glob }}
              title={`Purge everything under ${n.path}`}
            >
              Purge…
            </Link>
          )}
        </div>
        {isOpen && children(n, depth + 1)}
      </div>
    );
  };

  const children = (n: Node, depth: number) => (
    <div role="group">
      {[...n.dirs.values()]
        .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }))
        .map((d) => renderNode(d, depth))}
      {[...n.files]
        .sort((a, b) => a.rel.localeCompare(b.rel, undefined, { numeric: true }))
        .map((f) => {
          const one = emptyRollup();
          add(one, f);
          return (
            <Link
              key={"f:" + f.doc.path}
              className="dt-row dt-file"
              style={{ paddingLeft: 14 + depth * 18 + 22 }}
              to="/i/$index/d/$doc/pages"
              params={{ index, doc: f.rel }}
            >
              <span className="dt-fname" title={f.rel}>
                {fileName(f.rel)}
              </span>
              <span className="dt-ftitle muted">{f.title !== fileName(f.rel) ? f.title : ""}</span>
              {f.kind && <span className="badge dl-kind">{kindLabel(f.kind)}</span>}
              <Stats sum={one} folder={false} />
            </Link>
          );
        })}
    </div>
  );

  return (
    <div className="dt" role="tree" aria-label="Documents by folder">
      <div className="dt-row dt-root">
        <Chips sum={tree.sum} />
        <Stats sum={tree.sum} folder />
      </div>
      {children(tree, 0)}
    </div>
  );
}
