import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { getEmailThread, type EmailMessage, type EmailAttachment } from "../api";

// A mail archive as a conversation.
//
// The Pages tab renders one message per page, which is exactly what the INDEX
// holds and is unreadable as a thread: 26 undifferentiated blocks of text, the
// enclosure marked by a line of dashes, the headers as prose, "(43 further
// headers)" that cannot be opened, and an attachment as a bare filename even
// though the file was extracted and indexed as its own document.
//
// Everything here was already parsed and then flattened for the index. This
// reads the archive again — see /api/email for why from the file rather than
// from the stored page text — and shows the structure that was thrown away:
// enclosure depth as indentation, every header on demand, and each attachment as
// a link to the document it became.
export function Email() {
  const { index, doc } = useParams({ from: "/i/$index/d/$doc" });
  const [msgs, setMsgs] = useState<EmailMessage[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    setError("");
    setMsgs(null);
    getEmailThread(index, doc)
      .then((r) => live && setMsgs(r.messages ?? []))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index, doc]);

  if (error) return <div className="empty err">{error}</div>;
  if (!msgs) return <div className="empty">reading the archive…</div>;
  if (!msgs.length) return <div className="empty">This archive carries no messages.</div>;

  const attachments = msgs.reduce((n, m) => n + (m.attachments?.length ?? 0), 0);

  return (
    <>
      <p className="why" style={{ padding: ".5rem .75rem 0" }}>
        {msgs.length} message{msgs.length === 1 ? "" : "s"}
        {attachments ? `, carrying ${attachments} attachment${attachments === 1 ? "" : "s"}` : ""}.
        An enclosed message is one that travelled inside another — it is indented
        under the message that carried it.
      </p>
      {msgs.map((m) => (
        <Message key={m.page} m={m} index={index} doc={doc} />
      ))}
    </>
  );
}

function Message({ m, index, doc }: { m: EmailMessage; index: string; doc: string }) {
  const [showHeaders, setShowHeaders] = useState(false);
  const [showBody, setShowBody] = useState(true);
  // Indentation IS the thread. Capped so a deeply forwarded chain does not walk
  // off the right edge — depth is still stated in the badge.
  const indent = Math.min(m.depth, 6) * 1.25;

  return (
    <div className="msg" style={{ marginLeft: `${indent}rem` }}>
      <div className="msghead">
        <button className="expand" onClick={() => setShowBody((v) => !v)} title="collapse this message">
          {showBody ? "▾" : "▸"}
        </button>
        <strong>{m.subject || "(no subject)"}</strong>
        {m.depth > 0 && (
          <span className="badge" title="this message travelled inside another">
            enclosed ×{m.depth}
          </span>
        )}
        {/* The page is the citation. A search hit names a page, and this is how
            a reader gets from the thread back to the indexed text and vice
            versa. */}
        <Link
          className="badge"
          to="/i/$index/d/$doc/pages/$page"
          params={{ index, doc, page: String(m.page) }}
          title="the indexed page for this message"
        >
          p{m.page}
        </Link>
      </div>

      <div className="msgfrom">
        {m.from && (
          <span>
            <span className="k">from</span> {m.from}
          </span>
        )}
        {m.to && (
          <span>
            <span className="k">to</span> {m.to}
          </span>
        )}
        {m.cc && (
          <span>
            <span className="k">cc</span> {m.cc}
          </span>
        )}
        {m.date && (
          <span>
            <span className="k">date</span> {m.date}
          </span>
        )}
      </div>

      {!!m.headers?.length && (
        <div className="acts">
          <button className="act" onClick={() => setShowHeaders((v) => !v)}>
            {showHeaders ? "Hide" : "Show"} all {m.headers.length} headers
          </button>
        </div>
      )}
      {showHeaders && (
        <table className="hdrs">
          <tbody>
            {m.headers!.map((h, i) => (
              <tr key={`${h.name}-${i}`}>
                <td className="k">{h.name}</td>
                <td className="det">{h.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {!!m.attachments?.length && (
        <div className="atts">
          {m.attachments.map((a, i) => (
            <Attachment key={`${a.sum || a.name}-${i}`} a={a} index={index} />
          ))}
        </div>
      )}

      {/* Verbatim, wrapped, monospace-free: this is somebody's letter. Rendered
          as TEXT — a message body can contain markup and it is content, not
          instructions to the browser. */}
      {showBody && m.body && <div className="msgbody">{m.body}</div>}

      {/* Structure the reader could not parse, carried rather than dropped: a
          transcription that loses content silently is worse than one that says
          it did, because only the second is recoverable. */}
      {m.notes?.map((n, i) => (
        <div className="err" key={i}>
          {n}
        </div>
      ))}
    </div>
  );
}

function Attachment({ a, index }: { a: EmailAttachment; index: string }) {
    // An attachment that was EXTRACTED is a document in its own right — it was
    // ingested, captioned and indexed — so it links there. One that was not is
    // still evidence ("a 4 MB PDF called survey.pdf was sent on this date") and
    // must not be rendered as if the file were missing or the attachment absent.
  const label = a.name || "(unnamed)";
  const meta = [a.mime, a.size ? bytes(a.size) : ""].filter(Boolean).join(" · ");
  if (!a.path) {
    return (
      <span className="att" title={`declared ${a.mime || "unknown type"}; not extracted from the archive`}>
        📎 {label} <span className="muted">{meta} — not extracted</span>
      </span>
    );
  }
  return (
    <Link className="att on" to="/i/$index/d/$doc/pages" params={{ index, doc: a.path }}
          title={a.path}>
      📎 {label} <span className="muted">{meta}</span>
    </Link>
  );
}

function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}
