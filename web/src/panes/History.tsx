import { useDoc } from "../useDocDetail";
import { engineLabel } from "../ui/display";

// What has happened to this document: when it was read into the index and
// from where, readings taken, and verdicts ruled.
//
// Ordered as the server returned it. These rows accumulate and are never
// rewritten — a later correction does not replace an earlier reading, it is
// another fact about the same page — so the list IS the record, not a view of a
// current state.
export function History() {
  const { detail } = useDoc();
  const events = detail.history ?? [];
  if (!events.length) {
    return <div className="empty">Nothing recorded for this document yet.</div>;
  }
  // The first ingest added the document; every later one read it again.
  const firstIngest = events.findIndex((e) => e.kind === "ingest");
  return (
    <table className="seen">
      <thead>
        <tr>
          <th>Event</th>
          <th className="num">Page</th>
          <th>Source</th>
          <th>By</th>
          <th>When</th>
          <th>Note</th>
        </tr>
      </thead>
      <tbody>
        {events.map((e, i) => (
          <tr key={`${e.kind}-${e.seq ?? i}-${e.page ?? ""}-${i}`}>
            <td>
              {e.kind === "ingest" && i !== firstIngest ? "Read again" : KIND_LABELS[e.kind] ?? e.kind}
              {/* "active" is which reading is the one in the index right now.
                  Without it a page with three readings gives no clue which one
                  search is actually matching against. */}
              {e.active && <span className="badge" style={{ marginLeft: 6 }} title="This is the reading search uses now">current</span>}
            </td>
            <td className="num">{e.page || ""}</td>
            <td>
              {e.kind === "reading"
                ? sourceLabel(e.source)
                : e.kind === "ingest"
                  ? e.source === "error"
                    ? "Failed"
                    : "Read in full"
                  : e.source || ""}
            </td>
            <td>{e.by || <span className="muted">—</span>}</td>
            <td className="muted">{when(e.at)}</td>
            <td style={{ whiteSpace: "pre-wrap" }}>
              {e.note || ""}
              {e.kind === "ingest" && e.text && <div className="muted">{e.text}</div>}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const KIND_LABELS: Record<string, string> = {
  ingest: "Added to the index",
  reading: "Page reading",
  verdict: "Review decision",
};

const whenFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

// An RFC 3339 stamp in the reader's own words and time zone; anything else as
// it came.
function when(at?: string): string {
  if (!at) return "";
  const d = new Date(at);
  return Number.isNaN(d.getTime()) ? at : whenFmt.format(d);
}

// How a reading was made, in words. "corrected" is a person's correction and is
// said so; an engine name goes through the shared engine labels.
function sourceLabel(source?: string): string {
  if (!source) return "";
  if (source === "corrected") return "Corrected by a person";
  return engineLabel(source);
}
