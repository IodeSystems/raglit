import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { getPageLayout, postJSON, type DocPage, type PageLayout } from "../api";
import { useDoc } from "../useDocDetail";
import { NotesPanel } from "./Notes";
import { ErrorText } from "../ui/Details";
import { engineLabel } from "../ui/display";
import "./Pages.css";

// The page list: every page's image beside the text that was indexed from it.
//
// Each row is a LINK into that page, not a scroll anchor. A page of a bundle is
// the unit somebody actually wants to send you — "the signature is on page 14" —
// and it is also the only scope at which a note about a bundle means anything.
// The earlier version highlighted a row and went no further, which left the page
// route in the URL doing nothing a reader could see.
export function Pages() {
  const { detail, index, doc } = useDoc();
  const [lightbox, setLightbox] = useState<string | null>(null);

  const pages = detail.pages ?? [];
  if (!pages.length) {
    return (
      <div className="empty">
        This document is not shown page by page. Files with built-in text are
        indexed whole; a scanned document gets pages once its images have been read.
      </div>
    );
  }

  return (
    <div>
      {pages.map((p) => (
        <div className="pagecard" key={p.page}>
          <PageImageWithLayout p={p} onZoom={setLightbox} />
          <div>
            <div className="pagehead">
              <Link
                className="n"
                to="/i/$index/d/$doc/pages/$page"
                params={{ index, doc, page: String(p.page) }}
              >
                Page {p.page} →
              </Link>
              <PageBadges p={p} />
            </div>
            <PageBody p={p} index={index} doc={doc} />
          </div>
        </div>
      ))}
      {lightbox && <Lightbox src={lightbox} onClose={() => setLightbox(null)} />}
    </div>
  );
}

// One page, on its own: the image, what was read from it, what the machine saw,
// and the notes anchored to it.
//
// This is where a per-page note is written, because it is the only screen where
// "add a note" can mean exactly one thing.
export function PageDetail({ page }: { page: number }) {
  const { detail, index, doc } = useDoc();
  const [lightbox, setLightbox] = useState<string | null>(null);
  const pages = detail.pages ?? [];
  const p = pages.find((x) => x.page === page);
  const at = pages.findIndex((x) => x.page === page);
  const prev = at > 0 ? pages[at - 1] : undefined;
  const next = at >= 0 && at < pages.length - 1 ? pages[at + 1] : undefined;

  if (!p) {
    return (
      <div className="empty">
        This document has no page {page}.{" "}
        <Link to="/i/$index/d/$doc/pages" params={{ index, doc }}>
          All pages
        </Link>
      </div>
    );
  }

  return (
    <>
      <div className="pagenav">
        <Link to="/i/$index/d/$doc/pages" params={{ index, doc }}>
          ← All pages
        </Link>
        <div className="grow" />
        {/* Previous/next by POSITION in the list, not by page ± 1. A sliced or
            partly-read document has gaps, and stepping into one lands on a page
            that says it does not exist. */}
        {prev && (
          <Link to="/i/$index/d/$doc/pages/$page"
                params={{ index, doc, page: String(prev.page) }}>
            ← Page {prev.page}
          </Link>
        )}
        <span className="muted">
          Page {p.page} of {pages.length}
        </span>
        {next && (
          <Link to="/i/$index/d/$doc/pages/$page"
                params={{ index, doc, page: String(next.page) }}>
            Page {next.page} →
          </Link>
        )}
      </div>

      <div className="pagecard">
        <PageImageWithLayout p={p} onZoom={setLightbox} />
        <div>
          <div className="pagehead">
            <span className="n">Page {p.page}</span>
            <PageBadges p={p} />
          </div>
          <PageBody p={p} index={index} doc={doc} tall />

          {!!p.figures?.length && (
            <div style={{ marginTop: 10 }}>
              {/* What the machine SAW here, beside what it read. A transcript
                  that says "[FIGURE: a survey map …]" and shows nothing is a
                  dead end. */}
              <div className="muted">
                {p.figures.length} figure{p.figures.length > 1 ? "s" : ""} described on this page (AI description)
              </div>
              {p.figures.map((f, i) => (
                <div className="prov" style={{ marginTop: 6 }} key={i}>
                  {f.kind && <div className="muted">{f.kind}</div>}
                  <div>{f.description || "(no description)"}</div>
                </div>
              ))}
              {/* No bbox is recorded, so there is no crop to show and the page
                  image IS the figure. Said rather than implied. */}
              <div className="muted">
                No region is recorded for a figure — the page image is what was described.
              </div>
            </div>
          )}

          {(p.read_by || p.read_at) && (
            <div className="muted" style={{ fontSize: 11 }}>
              {[p.read_by, p.read_at].filter(Boolean).join(" · ")}
            </div>
          )}
        </div>
      </div>

      <h2 style={{ margin: "0 14px" }}>Notes on page {p.page}</h2>
      <NotesPanel pageScope={p.page} />
      {lightbox && <Lightbox src={lightbox} onClose={() => setLightbox(null)} />}
    </>
  );
}

function PageBadges({ p }: { p: DocPage }) {
  return (
    <>
      {/* A person's correction outranks whatever the machine read, so it is
          coloured apart rather than left to look like another engine. */}
      {p.source && (
        <span className={"badge " + (p.source === "corrected" ? "vision" : "text")}>
          {p.source === "corrected" ? "Corrected by a person" : engineLabel(p.source)}
        </span>
      )}
      {p.engine && p.engine !== p.source && (
        <span className="badge eng" title={p.engine}>
          {engineLabel(p.engine)}
        </span>
      )}
    </>
  );
}

function PageImage({
  p,
  onZoom,
  failed,
}: {
  p: DocPage;
  onZoom: (src: string) => void;
  failed?: boolean;
}) {
  const { index, doc } = useDoc();
  const [broken, setBroken] = useState(!!failed);
  // Bumped by "Try again": the same URL would be served from the failed cache
  // entry, so the retry asks for it under a new query string.
  const [attempt, setAttempt] = useState(0);
  const src = p.image_url ? retryURL(p.image_url, attempt) : "";
  return (
    <div>
      {p.image_url && !broken ? (
        // NOT loading="lazy". A page image has no intrinsic size until it loads,
        // and in this grid an unloaded one is zero-height — so the browser
        // decides it is not near the viewport, never fetches it, and it stays
        // zero-height. The lazy attribute deadlocks against its own effect.
        <img
          src={src}
          alt={`Page ${p.page}`}
          onError={() => setBroken(true)}
          onClick={() => onZoom(src)}
        />
      ) : (
        <PagePlaceholder
          page={p.page}
          failed={broken}
          onRetry={
            broken
              ? () => {
                  setAttempt((n) => n + 1);
                  setBroken(false);
                }
              : undefined
          }
        />
      )}
      {p.image_url && !broken && <Reocr index={index} doc={doc} page={p.page} />}
    </div>
  );
}

// What stands where a page image would be. A failed load and a page that never
// had an image are different facts, and the text beside it is still the indexed
// reading either way, so the placeholder says so instead of looking like an
// error in the document.
function PagePlaceholder({
  page,
  failed,
  onRetry,
}: {
  page: number;
  failed: boolean;
  onRetry?: () => void;
}) {
  return (
    <div className="page-ph" role="img" aria-label={`Page ${page}: no image shown`}>
      <div className="page-ph-icon" aria-hidden="true" />
      <div className="page-ph-title">
        {failed ? "Page image unavailable" : "No image for this page"}
      </div>
      <div className="page-ph-sub">
        {failed
          ? "The image could not be loaded. The text beside it is unaffected."
          : "This page was indexed from its text only."}
      </div>
      {onRetry && (
        <button className="linkish" onClick={onRetry}>
          Try again
        </button>
      )}
    </div>
  );
}

function retryURL(url: string, attempt: number): string {
  if (!attempt) return url;
  return url + (url.includes("?") ? "&" : "?") + "retry=" + attempt;
}

// Re-OCR stays beside the page rather than moving to the workbench: this is the
// MACHINE having another go, which is a different act from a person ruling on
// what it said. Conflating them would let a re-read look like a review.
function Reocr({ index, doc, page }: { index: string; doc: string; page: number }) {
  const [busy, setBusy] = useState(false);
  const [out, setOut] = useState<{ text?: string; err?: string } | null>(null);

  return (
    <div className="reocr">
      <button
        disabled={busy}
        title="Read this page's image again and show the result here. The indexed text does not change."
        onClick={async () => {
          setBusy(true);
          setOut(null);
          try {
            const r = await postJSON<{ text?: string }>("/api/reocr", { index }, { path: doc, page });
            setOut({ text: r.text || "No text was found on this page." });
          } catch (e: unknown) {
            setOut({ err: String(e) });
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? "Reading…" : "Read page again"}
      </button>
      {out && (
        <div className="out">
          {out.err ? <ErrorText raw={out.err} /> : <div className="ptext">{out.text}</div>}
        </div>
      )}
    </div>
  );
}

function Lightbox({ src, onClose }: { src: string; onClose: () => void }) {
  return (
    <dialog ref={(el) => el?.showModal()} onClose={onClose} onClick={onClose}>
      <img src={src} alt="" />
    </dialog>
  );
}

// ── layout blocks ──────────────────────────────────────────────────────
//
// What the READER saw, beside what the INDEX kept. The pipeline strips layout
// markup before indexing (it was 40% of some pages' bytes and it truncated the
// segmenter), which is right — but it left the boxes with no surface at all.
// This is that surface, on the page screen that already exists rather than a
// second one beside it.

// pageLayoutCache keeps one page's layout for the life of the view, so switching
// tabs does not refetch and the image overlay and the block list cannot disagree.
function useLayout(index: string, doc: string, page: number) {
  const [pl, setPl] = useState<PageLayout | null>(null);
  const [err, setErr] = useState(false);
  useEffect(() => {
    let live = true;
    setPl(null);
    setErr(false);
    getPageLayout(index, doc, page)
      .then((v) => live && setPl(v))
      .catch(() => live && setErr(true));
    return () => {
      live = false;
    };
  }, [index, doc, page]);
  return { pl, err };
}

// The image with the reader's blocks drawn on it.
//
// Boxes are placed as PERCENTAGES: the coordinates are normalised 0-1000 per
// axis independently, so no image dimensions are needed and the overlay stays
// aligned at any width — including when the image is still loading, which is
// when a pixel-based overlay would be wrong.
function PageImageWithLayout({
  p,
  onZoom,
}: {
  p: DocPage;
  onZoom: (src: string) => void;
}) {
  const { index, doc } = useDoc();
  const { pl } = useLayout(index, doc, p.page);
  const [show, setShow] = useState(true);
  const [broken, setBroken] = useState(false);
  const boxes = pl?.boxes ?? [];

  if (!p.image_url || broken) return <PageImage p={p} onZoom={onZoom} failed={broken} />;
  return (
    <div>
      <div style={{ position: "relative", display: "block" }}>
        <img
          src={p.image_url}
          alt={`Page ${p.page}`}
          onError={() => setBroken(true)}
          onClick={() => onZoom(p.image_url!)}
          style={{ display: "block", width: "100%" }}
        />
        {show &&
          boxes.map((b, i) => (
            <div
              key={i}
              title={`${b.label || "block"}: ${(b.text || "").slice(0, 120)}`}
              data-box={i}
              style={{
                position: "absolute",
                left: `${b.x0 / 10}%`,
                top: `${b.y0 / 10}%`,
                width: `${(b.x1 - b.x0) / 10}%`,
                height: `${(b.y1 - b.y0) / 10}%`,
                border: "1.5px solid rgba(224,0,170,.55)",
                background: "rgba(224,0,170,.06)",
                pointerEvents: "none",
              }}
            />
          ))}
      </div>
      {!!boxes.length && (
        <label className="muted" style={{ display: "block", marginTop: 6 }}>
          <input type="checkbox" checked={show} onChange={(e) => setShow(e.target.checked)} />{" "}
          {boxes.length} layout block{boxes.length > 1 ? "s" : ""}
        </label>
      )}
      <Reocr index={index} doc={doc} page={p.page} />
    </div>
  );
}

// Text or Layout, on the same card.
//
// Two tabs ONLY where there is a layout to show — `has_layout` comes back with
// the page list so the tab strip does not appear over a tesseract page and then
// turn out empty. Text is the default because it is what search matched; Layout
// is what the reader saw, which is the thing you go looking for when the text
// is wrong and you want to know where it came from.
function PageBody({
  p,
  index,
  doc,
  tall,
}: {
  p: DocPage;
  index: string;
  doc: string;
  tall?: boolean;
}) {
  const [tab, setTab] = useState<"text" | "layout">("text");
  const style = tall ? { maxHeight: "none" as const } : undefined;
  if (!p.has_layout) {
    return (
      <div className="ptext" style={style}>
        {p.text || <span className="muted">No text on this page.</span>}
      </div>
    );
  }
  return (
    <div>
      <div className="tabstrip">
        <button className={tab === "text" ? "on" : ""} onClick={() => setTab("text")}>
          Text
        </button>
        <button className={tab === "layout" ? "on" : ""} onClick={() => setTab("layout")}>
          Layout
        </button>
      </div>
      {tab === "text" ? (
        <div className="ptext" style={style}>
          {p.text || <span className="muted">No text on this page.</span>}
        </div>
      ) : (
        <LayoutBlocks index={index} doc={doc} page={p.page} />
      )}
    </div>
  );
}

// The page, REBUILT from the reader's blocks.
//
// A flat list of blocks is a list of micro-fragments: it tells you what was read
// but not how the page was laid out, which is the question the layout data
// exists to answer. This places each block's text at its own box, sized to fit,
// so a form reads as a form and a title block sits where the title block is.
//
// Sizing: the first guess comes from the box HEIGHT (one line filling its box),
// then a fit pass shrinks any block whose text overflows. Guessing from height
// alone is wrong for a wide box holding one short word and for a narrow box
// holding a sentence, and both are common on a form.
function ReconstructedPage({ pl }: { pl: PageLayout }) {
  const boxes = pl.boxes ?? [];
  const ref = useRef<HTMLDivElement | null>(null);
  const ratio = pl.img_w && pl.img_h ? pl.img_w / pl.img_h : 0;

  // Fit each block's text to its own box, in real pixels.
  //
  // The first version guessed from the box HEIGHT and then shrank by 12% a step
  // at a time. A one-line box was fine; a paragraph in a short wide box — a legal
  // description, which is most of a deed — overflowed immediately and shrank
  // through twenty steps to 4% of its starting size. Unreadable, and the page
  // looked like grey dust.
  //
  // So: seed from the box's AREA against the character count (how big can a
  // glyph be if N of them must tile this rectangle), clamp to the box height for
  // a single line, then BINARY SEARCH the largest size that does not overflow.
  // Bounded at ~7 steps instead of an open-ended shrink, and it fills the box
  // rather than merely fitting inside it.
  useEffect(() => {
    const root = ref.current;
    if (!root) return;
    const fit = () => {
      const W = root.clientWidth,
        H = root.clientHeight;
      if (!W || !H) return;
      for (const el of Array.from(root.querySelectorAll<HTMLElement>("[data-fit]"))) {
        const bw = (parseFloat(el.dataset.w || "0") / 100) * W;
        const bh = (parseFloat(el.dataset.h || "0") / 100) * H;
        const n = Math.max(1, (el.textContent || "").length);
        // ~0.5 aspect per glyph: n glyphs of size s tile s*0.5*s*1.2 each.
        const seed = Math.min(bh, Math.sqrt((bw * bh) / (n * 0.6)));
        let lo = 3,
          hi = Math.max(4, Math.min(seed * 1.6, bh));
        const fits = (s: number) => {
          el.style.fontSize = `${s}px`;
          return el.scrollHeight <= el.clientHeight + 1 && el.scrollWidth <= el.clientWidth + 1;
        };
        if (fits(hi)) continue;
        for (let i = 0; i < 7; i++) {
          const mid = (lo + hi) / 2;
          if (fits(mid)) lo = mid;
          else hi = mid;
        }
        el.style.fontSize = `${lo}px`;
      }
    };
    fit();
    // The container is percentage-width, so its pixel size changes with the
    // window and every fitted size goes stale with it.
    const ro = new ResizeObserver(fit);
    ro.observe(root);
    return () => ro.disconnect();
  }, [pl]);

  if (!ratio) {
    return (
      <div className="muted">
        The page image's size is unknown, so its shape cannot be reconstructed.
      </div>
    );
  }
  return (
    <div
      ref={ref}
      className="reconstruct"
      style={{ aspectRatio: String(ratio) }}
      title="Rebuilt from the layout the reader found. This is not the page image."
    >
      {boxes.map((b, i) => {
        const h = (b.y1 - b.y0) / 10;
        return (
          <div
            key={i}
            data-fit
            data-label={b.label}
            data-w={(b.x1 - b.x0) / 10}
            data-h={h}
            style={{
              position: "absolute",
              left: `${b.x0 / 10}%`,
              top: `${b.y0 / 10}%`,
              width: `${(b.x1 - b.x0) / 10}%`,
              height: `${h}%`,
              overflow: "hidden",
              lineHeight: 1.12,
            }}
          >
            {b.text}
          </div>
        );
      })}
    </div>
  );
}

// The rebuilt page, and the raw transcription against the indexed text.
//
// The byte counts are shown because the difference is the point: a page whose
// raw and indexed text differ sharply is one where the reader saw structure the
// index does not hold, and that is worth knowing before quoting either.
function LayoutBlocks({ index, doc, page }: { index: string; doc: string; page: number }) {
  const { pl, err } = useLayout(index, doc, page);
  const [tab, setTab] = useState<"blocks" | "raw">("blocks");
  if (err || !pl) return null;
  const boxes = pl.boxes ?? [];
  if (!boxes.length && !pl.raw) return null;
  return (
    <div style={{ marginTop: 12 }}>
      <div
        className="muted"
        style={{ marginBottom: 6 }}
        title={[
          pl.model || pl.engine ? `read by ${pl.model || pl.engine}` : "",
          `raw reading ${(pl.raw || "").length} bytes, indexed text ${(pl.indexed || "").length} bytes`,
        ]
          .filter(Boolean)
          .join(" · ")}
      >
        {boxes.length} layout block{boxes.length === 1 ? "" : "s"}{" "}
        <button className="linkish" onClick={() => setTab(tab === "blocks" ? "raw" : "blocks")}>
          {tab === "blocks" ? "Show raw reading" : "Show rebuilt page"}
        </button>
      </div>
      {tab === "blocks" ? (
        <div>
          {boxes.length ? (
            <ReconstructedPage pl={pl} />
          ) : (
            <div className="muted">
              This page has no layout blocks — the engine that read it does not emit them.
            </div>
          )}
        </div>
      ) : (
        <pre className="ptext" style={{ whiteSpace: "pre-wrap", maxHeight: "50vh", overflow: "auto" }}>
          {pl.raw}
        </pre>
      )}
    </div>
  );
}
