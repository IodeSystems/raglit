import {
  Link,
  useNavigate,
  useParams,
  useSearch,
} from "@tanstack/react-router";
import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";

import { search, type Hit } from "../api";
import { ErrorText } from "../ui/Details";
import { AISummaryLabel, AITitleLabel } from "../ui/Label";
import { useIndexInfo } from "./IndexRows";
import { SEARCH_MODES, displayPath, displayTitle, fileName, isOtherKind, kindLabel, passages } from "../ui/display";
import "./Search.css";
import { useResetOn } from "../useResetOn";

// ── shaping the hits ─────────────────────────────────────────────────────────

// One card per document. The daemon ranks passages, and the same document can
// hold the top five places; listed flat, that read as one result five times.
// Groups keep the order of their best passage, so the card order is still the
// ranking, and inside a card the passages stay in rank order.
type DocGroup = { key: string; index: string; hits: Hit[] };

function groupHits(hits: Hit[], fallbackIndex: string): DocGroup[] {
  const byKey = new Map<string, DocGroup>();
  const out: DocGroup[] = [];
  for (const h of hits) {
    const index = h.index || fallbackIndex;
    const key = `${index}\u0000${h.doc_id}`;
    let g = byKey.get(key);
    if (!g) {
      g = { key, index, hits: [] };
      byKey.set(key, g);
      out.push(g);
    }
    g.hits.push(h);
  }
  return out;
}

// The bracketed line identityFragmentText (identity.go) puts in front of every
// generated caption. It is there so the text says what it is wherever it
// travels; here the AI-summary label already says so, and saying it twice is
// noise.
const PREAMBLE = /^\s*\[what this document is[^\]]*\]\s*/i;
// A court pleading's margin numbers ("1 2 3 … 32") read first by OCR.
const LINE_NUMBERS = /^(?:\d{1,2}\s+){6,}/;

function cleanSnippet(s: string): string {
  return s.replace(PREAMBLE, "").replace(LINE_NUMBERS, "").trim();
}


// ── highlighting ─────────────────────────────────────────────────────────────

const STOPWORDS = new Set(
  "a an and are as at be by for from in is it of on or that the this to was with".split(
    " ",
  ),
);

// The query's words, longest first so "surveying" wins over "survey" where both
// were typed. Search syntax (quotes, -, *, :) is dropped.
function queryTerms(q: string): string[] {
  const words = q
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter((w) => w.length >= 2 && !STOPWORDS.has(w));
  return [...new Set(words)].sort((a, b) => b.length - a.length);
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// Marks each query word where a word STARTS with it, through the end of that
// word, so "survey" marks "surveying" too (the index stems; the reader expects
// the same). The result is React text nodes and <mark>s, never HTML: a snippet
// is corpus text and a corpus can contain markup.
function highlight(text: string, re: RegExp | null): ReactNode {
  if (!re) return text;
  const parts = text.split(re);
  return parts.map((p, i) =>
    i % 2 === 1 ? <mark key={i}>{p}</mark> : <Fragment key={i}>{p}</Fragment>,
  );
}

function termRegex(terms: string[]): RegExp | null {
  if (terms.length === 0) return null;
  return new RegExp(
    `(?<![\\p{L}\\p{N}])((?:${terms.map(escapeRe).join("|")})[\\p{L}\\p{N}]*)`,
    "giu",
  );
}

// The daemon says "HybridSearch needs an embedder" when no embedding model is
// configured. That is a fact about the server, not a failure of the query.
function noMeaningSearch(err: string): boolean {
  return /needs an embedder|SetEmbedder/i.test(err);
}

// ── the pane ─────────────────────────────────────────────────────────────────

// Search results for a query that lives in the URL.
//
// The query is a search param rather than component state, so a search is a link
// and the back button walks searches. Somebody finding the right query for a
// corpus can now send it to somebody else, which is the ordinary thing people
// wanted to do and could not.
//
// ONE implementation, two scopes. The daemon's `index` parameter already takes a
// comma list and a `prefix*` wildcard, and selectIndexes expands it — so
// searching a whole project is the string "<project>__*" and needs no endpoint
// of its own. What differs between the two callers is only what they put in the
// URL when the mode changes, which is the onMode callback.
// inScope applies the daemon's index selector — a comma list of names and
// "prefix*" wildcards — to one index name.
function inScope(name: string, scope: string): boolean {
  return scope.split(",").some((t) => {
    const s = t.trim();
    return s.endsWith("*") ? name.startsWith(s.slice(0, -1)) : name === s;
  });
}

function SearchPane({
  scope,
  q,
  mode,
  onMode,
  fallbackIndex,
}: {
  scope: string;
  q: string;
  mode: string;
  onMode: (mode: string) => void;
  // Where a hit links when it does not name its own index. The daemon always
  // sends one; this is what keeps an older daemon's results clickable rather
  // than routing them to `undefined`.
  fallbackIndex: string;
}) {
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<Set<string>>(new Set());

  useResetOn([scope, q, mode], () => {
    setOpen(new Set());
    setError("");
    setHits(null);
  });
  useEffect(() => {
    if (!q) return;
    let live = true;
    search(scope, q, mode)
      .then((r) => live && setHits(r.hits ?? []))
      .catch(
        (e: unknown) =>
          live && setError(e instanceof Error ? e.message : String(e)),
      );
    return () => {
      live = false;
    };
  }, [scope, q, mode]);

  const groups = useMemo(
    () => (hits ? groupHits(hits, fallbackIndex) : []),
    [hits, fallbackIndex],
  );
  const re = useMemo(() => termRegex(queryTerms(q)), [q]);

  // A project search spans indexes, so a hit has to say which one it is from —
  // otherwise two documents of the same name in two indexes are one row twice.
  const spansIndexes = scope.includes("*") || scope.includes(",");

  const toggle = (key: string) =>
    setOpen((s) => {
      const n = new Set(s);
      if (n.has(key)) n.delete(key);
      else n.add(key);
      return n;
    });

  // Offer search by meaning only where some index in scope can run it.
  const info = useIndexInfo();
  const meaning = [...info.values()].some((i) => i.meaning && inScope(i.name, scope));
  const modes = SEARCH_MODES.filter((m) => m.value === "bm25" || meaning || m.value === mode);
  const modeOption = SEARCH_MODES.find((m) => m.value === mode);

  return (
    <div className="panel">
      <div className="searchbar">
        <span className="sr-summary" aria-live="polite">
          {!q ? (
            "Type a search above."
          ) : hits === null ? (
            error ? (
              ""
            ) : (
              <>Searching for “{q}”…</>
            )
          ) : hits.length === 0 ? (
            ""
          ) : (
            <>
              {groups.length.toLocaleString()}{" "}
              {groups.length === 1 ? "document" : "documents"} ·{" "}
              {passages(hits.length)} for “{q}”
            </>
          )}
        </span>
        <div className="grow" />
        <label className="sr-mode">
          Match
          <select
            value={mode}
            onChange={(e) => onMode(e.target.value)}
            title={modeOption?.hint}
          >
            {modes.map((m) => (
              <option key={m.value} value={m.value} title={m.hint}>
                {m.label}
              </option>
            ))}
            {!modeOption && <option value={mode}>{mode}</option>}
          </select>
        </label>
      </div>

      {!q && (
        <div className="sr-state">
          <div className="sr-state-title">Search this collection</div>
          Results are grouped by document, best match first.
        </div>
      )}

      {error && (
        <div className="sr-state">
          {noMeaningSearch(error) ? (
            <>
              <div className="sr-state-title">
                Search by meaning is not set up on this server
              </div>
              Only exact-word search is available.
              <div>
                <button className="sr-hintbtn" onClick={() => onMode("bm25")}>
                  Search exact words instead
                </button>
              </div>
            </>
          ) : (
            <>
              <div className="sr-state-title">The search did not complete</div>
              <ErrorText raw={error} />
            </>
          )}
        </div>
      )}

      {!error && q && hits === null && (
        <div aria-busy="true">
          {[0, 1, 2].map((i) => (
            <div className="sr-skel" key={i}>
              <div />
              <div />
              <div />
              <div />
            </div>
          ))}
        </div>
      )}

      {hits?.length === 0 && (
        <div className="sr-state">
          <div className="sr-state-title">No passages match “{q}”</div>
          Check the spelling, or use fewer words.
          {mode === "bm25" && (
            <div>
              <button className="sr-hintbtn" onClick={() => onMode("hybrid")}>
                Try matching by meaning too
              </button>
            </div>
          )}
        </div>
      )}

      {groups.map((g) => (
        <DocCard
          key={g.key}
          group={g}
          re={re}
          showIndex={spansIndexes}
          expanded={open.has(g.key)}
          onToggle={() => toggle(g.key)}
        />
      ))}
    </div>
  );
}

function DocCard({
  group,
  re,
  showIndex,
  expanded,
  onToggle,
}: {
  group: DocGroup;
  re: RegExp | null;
  showIndex: boolean;
  expanded: boolean;
  onToggle: () => void;
}) {
  const [best, ...rest] = group.hits;
  if (!best) return null;
  // rel is what the daemon resolves and what a person may read; doc_id is the
  // absolute indexed path and never goes into a URL or onto the screen.
  const rel = best.rel || displayPath(best.doc_id, best.root);
  const name = best.name ? displayTitle(best.name) : "";
  const title = name || best.title || fileName(rel);
  const machineName = !!name && best.name_source === "machine";
  const route = { index: group.index, doc: rel };

  return (
    <div className="sr-doc">
      <div className="sr-head">
        {/* Into the transcript, not the pages: a hit is a piece of TEXT, and
            landing on a page image makes the reader find the words again. */}
        <Link
          className="sr-title"
          to="/i/$index/d/$doc/transcript"
          params={route}
        >
          {title}
        </Link>
        {best.kind && !isOtherKind(best.kind) && (
          <span className="sr-kind">{kindLabel(best.kind)}</span>
        )}
        {machineName && <AITitleLabel model={best.name_model} />}
      </div>
      <div className="sr-sub">
        {showIndex && (
          <>
            {group.index}
            <span className="sr-sep">·</span>
          </>
        )}
        {/* The filename is the secondary line; it is the path relative to the
            index root, so two files of one name in two folders stay apart. */}
        <span>{rel}</span>
      </div>

      <Passage hit={best} re={re} route={route} />

      {rest.length > 0 && (
        <>
          <button
            className="sr-more"
            onClick={onToggle}
            aria-expanded={expanded}
          >
            {expanded ? "Fewer passages" : `+${passages(rest.length)} more`}
          </button>
          {expanded && (
            <div className="sr-rest">
              {rest.map((h, i) => (
                <Passage key={i} hit={h} re={re} route={route} />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function Passage({
  hit,
  re,
  route,
}: {
  hit: Hit;
  re: RegExp | null;
  route: { index: string; doc: string };
}) {
  // Say whose words these are, before the words themselves. A generated caption
  // and a model's description of a photograph rank in this list beside
  // transcribed text and would otherwise look exactly like it. In a corpus that
  // may be cited, that is the difference between "the fax says" and "the model
  // thinks the car is a Chevrolet". ONE label; the bracketed preamble the
  // daemon puts in the text is stripped so it is not said twice.
  const generated = !!hit.origin;
  const text = hit.summary || (hit.snippet ? cleanSnippet(hit.snippet) : "");
  return (
    <Link
      className="sr-passage"
      to="/i/$index/d/$doc/transcript"
      params={route}
    >
      {generated ? (
        <AISummaryLabel />
      ) : hit.page ? (
        <div className="sr-page">Page {hit.page}</div>
      ) : null}
      {text && (
        <div className={"sr-snip" + (generated ? " generated" : "")}>
          {highlight(text, re)}
        </div>
      )}
    </Link>
  );
}

export function Search() {
  const { index } = useParams({ from: "/i/$index" });
  const { q, mode } = useSearch({ from: "/i/$index/search" });
  const navigate = useNavigate();
  return (
    <SearchPane
      scope={index}
      fallbackIndex={index}
      q={q}
      mode={mode}
      onMode={(m) =>
        void navigate({
          to: "/i/$index/search",
          params: { index },
          search: { q, mode: m },
        })
      }
    />
  );
}

// Every index in the project at once. `<project>__*` is the selector the CLI
// already sends for "search all" within a namespace (nsSelector), so this asks
// the same question the same way.
export function ProjectSearch() {
  const { project } = useParams({ from: "/p/$project" });
  const { q, mode } = useSearch({ from: "/p/$project/search" });
  const navigate = useNavigate();
  return (
    <SearchPane
      scope={`${project}__*`}
      fallbackIndex={`${project}__default`}
      q={q}
      mode={mode}
      onMode={(m) =>
        void navigate({
          to: "/p/$project/search",
          params: { project },
          search: { q, mode: m },
        })
      }
    />
  );
}
