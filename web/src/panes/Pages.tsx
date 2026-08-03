import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { postJSON, type DocPage } from "../api";
import { useDoc } from "../useDocDetail";
import { NotesPanel } from "./Notes";

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
        No per-page reading. Born-digital text and plain files are indexed whole;
        a scanned sheet gets pages once it has been OCR'd or regioned.
      </div>
    );
  }

  return (
    <div>
      {pages.map((p) => (
        <div className="pagecard" key={p.page}>
          <PageImage p={p} onZoom={setLightbox} />
          <div>
            <div className="pagehead">
              <Link
                className="n"
                to="/i/$index/d/$doc/pages/$page"
                params={{ index, doc, page: String(p.page) }}
              >
                page {p.page} →
              </Link>
              <PageBadges p={p} />
            </div>
            <div className="ptext">{p.text || "(no text on this page)"}</div>
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
          ← all pages
        </Link>
        <div className="grow" />
        {/* Previous/next by POSITION in the list, not by page ± 1. A sliced or
            partly-read document has gaps, and stepping into one lands on a page
            that says it does not exist. */}
        {prev && (
          <Link to="/i/$index/d/$doc/pages/$page"
                params={{ index, doc, page: String(prev.page) }}>
            ← page {prev.page}
          </Link>
        )}
        <span className="muted">
          page {p.page} of {pages.length}
        </span>
        {next && (
          <Link to="/i/$index/d/$doc/pages/$page"
                params={{ index, doc, page: String(next.page) }}>
            page {next.page} →
          </Link>
        )}
      </div>

      <div className="pagecard">
        <PageImage p={p} onZoom={setLightbox} />
        <div>
          <div className="pagehead">
            <span className="n">page {p.page}</span>
            <PageBadges p={p} />
          </div>
          <div className="ptext" style={{ maxHeight: "none" }}>
            {p.text || "(no text on this page)"}
          </div>

          {!!p.figures?.length && (
            <div style={{ marginTop: 10 }}>
              {/* What the machine SAW here, beside what it read. A transcript
                  that says "[FIGURE: a survey map …]" and shows nothing is a
                  dead end. */}
              <div className="muted">
                {p.figures.length} figure{p.figures.length > 1 ? "s" : ""} described on this page
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
          {p.source}
        </span>
      )}
      {p.engine && <span className="badge eng">{p.engine}</span>}
    </>
  );
}

function PageImage({ p, onZoom }: { p: DocPage; onZoom: (src: string) => void }) {
  const { index, doc } = useDoc();
  const [broken, setBroken] = useState(false);
  return (
    <div>
      {p.image_url && !broken ? (
        // NOT loading="lazy". A page image has no intrinsic size until it loads,
        // and in this grid an unloaded one is zero-height — so the browser
        // decides it is not near the viewport, never fetches it, and it stays
        // zero-height. The lazy attribute deadlocks against its own effect.
        <img
          src={p.image_url}
          alt={`page ${p.page}`}
          onError={() => setBroken(true)}
          onClick={() => onZoom(p.image_url!)}
        />
      ) : (
        <div className="muted">
          {broken ? "(page image failed to load)" : "(no page image)"}
        </div>
      )}
      {p.image_url && !broken && <Reocr index={index} doc={doc} page={p.page} />}
    </div>
  );
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
        onClick={async () => {
          setBusy(true);
          setOut(null);
          try {
            const r = await postJSON<{ text?: string }>("/api/reocr", { index }, { path: doc, page });
            setOut({ text: r.text || "(empty)" });
          } catch (e: unknown) {
            setOut({ err: String(e) });
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? "running…" : "Re-OCR (cascade)"}
      </button>
      {out && (
        <div className="out">
          {out.err ? <div className="err">{out.err}</div> : <div className="ptext">{out.text}</div>}
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
