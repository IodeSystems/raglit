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
    return <div className="empty">Nothing indexed for this document.</div>;
  }
  return (
    <div className="dpane">
      <pre>{text}</pre>
    </div>
  );
}
