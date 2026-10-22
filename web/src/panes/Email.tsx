import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { getEmailThread, type EmailMessage, type EmailAttachment } from "../api";
import { useDoc } from "../useDocDetail";
import { ErrorText } from "../ui/Details";
import { displayPath } from "../ui/display";
import { useResetOn } from "../useResetOn";

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
  const { detail } = useDoc();
  const [msgs, setMsgs] = useState<EmailMessage[] | null>(null);
  const [error, setError] = useState("");

  useResetOn([index, doc], () => {
    setError("");
    setMsgs(null);
  });
  useEffect(() => {
    let live = true;
    getEmailThread(index, doc)
      .then((r) => live && setMsgs(r.messages ?? []))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index, doc]);

  if (error)
    return (
      <div className="empty">
        <ErrorText raw={error} />
      </div>
    );
  if (!msgs) return <div className="empty">Reading the messages…</div>;
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
        <Message key={m.page} m={m} index={index} doc={doc} root={detail.root} />
      ))}
    </>
  );
}

function Message({
  m,
  index,
  doc,
  root,
}: {
  m: EmailMessage;
  index: string;
  doc: string;
  root?: string;
}) {
  const [showHeaders, setShowHeaders] = useState(false);
  const [showBody, setShowBody] = useState(true);
  // Indentation IS the thread. Capped so a deeply forwarded chain does not walk
  // off the right edge — depth is still stated in the badge.
  const indent = Math.min(m.depth, 6) * 1.25;

  return (
    <div className="msg" style={{ marginLeft: `${indent}rem` }}>
      <div className="msghead">
        <button
          className="expand"
          onClick={() => setShowBody((v) => !v)}
          title={showBody ? "Collapse this message" : "Expand this message"}
        >
          {showBody ? "▾" : "▸"}
        </button>
        <strong>{m.subject || "(no subject)"}</strong>
        {m.depth > 0 && (
          <span className="badge" title="This message was forwarded or attached inside another">
            {m.depth === 1 ? "Enclosed" : `Enclosed, ${m.depth} levels deep`}
          </span>
        )}
        {/* The page is the citation. A search hit names a page, and this is how
            a reader gets from the thread back to the indexed text and vice
            versa. */}
        <Link
          className="badge"
          to="/i/$index/d/$doc/pages/$page"
          params={{ index, doc, page: String(m.page) }}
          title="The indexed page for this message"
        >
          Page {m.page}
        </Link>
      </div>

      <div className="msgfrom">
        {m.from && (
          <span>
            <span className="k">From</span> {m.from}
          </span>
        )}
        {m.to && (
          <span>
            <span className="k">To</span> {m.to}
          </span>
        )}
        {m.cc && (
          <span>
            <span className="k">Cc</span> {m.cc}
          </span>
        )}
        {m.date && (
          <span>
            <span className="k">Date</span> {m.date}
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
            <Attachment key={`${a.sum || a.name}-${i}`} a={a} index={index} root={root} />
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

function Attachment({ a, index, root }: { a: EmailAttachment; index: string; root?: string }) {
    // An attachment that was EXTRACTED is a document in its own right — it was
    // ingested, captioned and indexed — so it links there. One that was not is
    // still evidence ("a 4 MB PDF called survey.pdf was sent on this date") and
    // must not be rendered as if the file were missing or the attachment absent.
  const label = a.name || "(unnamed)";
  const meta = [a.mime, a.size ? bytes(a.size) : ""].filter(Boolean).join(" · ");
  if (!a.path) {
    return (
      <span className="att" title={`Declared as ${a.mime || "an unknown type"}; the file was not taken out of the message`}>
        📎 {label} <span className="muted">{meta} · not available as a document</span>
      </span>
    );
  }
  return (
    <Link className="att on" to="/i/$index/d/$doc/pages" params={{ index, doc: linkPath(a.path, root) }}
          title={`Open ${displayPath(linkPath(a.path, root), root)}`}>
      📎 {label} <span className="muted">{meta}</span>
    </Link>
  );
}

function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

// The daemon sends an attachment's ABSOLUTE path. The link carries the display
// path instead (F4): relative to the index root when it is under it, and
// "@<path under the index's storage>" for a file raglit extracted into its own
// storage — the same spelling the daemon uses and resolves (docpaths.go,
// StorageRel). Anything else goes through unchanged; the daemon still accepts it.
function linkPath(path: string, root?: string): string {
  if (root) {
    const r = root.replace(/\/+$/, "");
    if (path.startsWith(r + "/")) return path.slice(r.length + 1);
  }
  const m = path.match(/\/indexes\/[^/]+\/((?:attachments|uploads)\/.+)$/);
  if (m) return "@" + m[1];
  return path;
}
