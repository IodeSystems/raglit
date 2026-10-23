import { useState } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useDoc } from "../useDocDetail";

// The document's indexed text, as one run.
//
// This is what search actually matched against — not the file, and not the page
// images. Reading it is how somebody works out why a document did or did not
// come back for a query.
//
// Rendered (USER 2026-10-23): Markdown shown as Markdown. Raw stays one click
// away, because raw is what search matched. Rendered is the default for .md
// files and for page transcripts, which raglit writes as Markdown. No raw HTML
// is rendered, and images show as their alt text: a crawled page must not run
// script or load remote images in the review UI.
export function Transcript() {
  const { detail } = useDoc();
  const text = detail.text ?? "";
  const markdown = /\.(md|markdown)$/i.test(detail.path) || (detail.pages?.length ?? 0) > 0;
  const [rendered, setRendered] = useState(markdown);
  if (!text.trim()) {
    return <div className="empty">No text has been indexed for this document yet.</div>;
  }
  return (
    <div className="dpane">
      <div className="textmode" role="group" aria-label="text view">
        <button type="button" aria-pressed={rendered} onClick={() => setRendered(true)}>Rendered</button>
        <button type="button" aria-pressed={!rendered} onClick={() => setRendered(false)}>Raw</button>
      </div>
      {rendered ? (
        <div className="prose markdown">
          <Markdown
            remarkPlugins={[remarkGfm]}
            components={{
              a: ({ href, children }) => (
                <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>
              ),
              img: ({ alt }) => <span className="imgalt">[image{alt ? `: ${alt}` : ""}]</span>,
            }}
          >
            {text}
          </Markdown>
        </div>
      ) : (
        // Prose in the body font (F9); a <pre> only to keep the line breaks.
        <pre className="prose">{text}</pre>
      )}
    </div>
  );
}
