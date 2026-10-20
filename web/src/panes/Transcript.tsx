import { useDoc } from "../useDocDetail";

// The document's indexed text, as one run.
//
// This is what search actually matched against — not the file, and not the page
// images. Reading it is how somebody works out why a document did or did not
// come back for a query.
export function Transcript() {
  const { detail } = useDoc();
  const text = detail.text ?? "";
  if (!text.trim()) {
    return <div className="empty">No text has been indexed for this document yet.</div>;
  }
  return (
    <div className="dpane">
      {/* Prose in the body font (F9); a <pre> only to keep the line breaks. */}
      <pre className="prose">{text}</pre>
    </div>
  );
}
