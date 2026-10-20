import { useEffect, useState } from "react";

import { addNote, deleteNote, listNotes, type Note } from "../api";
import { useDoc } from "../useDocDetail";
import { ErrorText } from "../ui/Details";

// What a person knows about this document that the machine could not read off it.
//
// Distinct from a re-title and from an attestation, and the difference is worth
// keeping straight. A title says what the document IS. A verdict rules on a
// claim the machine made. A note is neither — it is context somebody carries in
// their head, and until there was somewhere to put it, it stayed there.
//
// A note may anchor to a page or to the whole document. Page-anchored because a
// bundle is not one thing: "the second exhibit starts here" is about page 40,
// and filing it against the document loses the only part that made it useful.

// The document-level tab: every note, whatever it is anchored to.
export function Notes() {
  return <NotesPanel />;
}

// pageScope pins the panel to one page — it lists only that page's notes and
// files new ones against it. Used by the per-page view, where "add a note" can
// only sensibly mean "about this page".
export function NotesPanel({ pageScope }: { pageScope?: number }) {
  const { index, doc, detail } = useDoc();
  const [notes, setNotes] = useState<Note[] | null>(null);
  const [err, setErr] = useState("");

  const load = () => {
    listNotes(index, doc)
      .then((r) => setNotes(r.notes ?? []))
      .catch((e: unknown) => setErr(String(e)));
  };
  useEffect(() => {
    setNotes(null);
    setErr("");
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [index, doc]);

  const pages = detail.pages ?? [];
  const shown =
    pageScope == null ? notes : (notes?.filter((n) => n.page === pageScope) ?? null);

  return (
    <>
      <NoteForm
        fixedPage={pageScope}
        maxPage={pages.length ? Math.max(...pages.map((p) => p.page)) : 0}
        onSubmit={async (body, author, page) => {
          await addNote(index, doc, { body, author, page });
          load();
        }}
      />
      {err && (
        <div className="empty">
          <ErrorText raw={err} />
        </div>
      )}
      {!err && shown === null && <div className="empty">Loading notes…</div>}
      {shown?.length === 0 && (
        <div className="empty">
          {pageScope == null
            ? "No notes yet. If this document is not what its title says it is, this is where to say so."
            : `No notes on page ${pageScope} yet.`}
        </div>
      )}
      {shown?.map((n) => (
        <div className="note" key={n.id}>
          <div className="meta">
            <b>{n.author || "Anonymous"}</b>
            {/* Only when it adds something. In a page-scoped list every note is
                about that page, and repeating it on each row is noise. */}
            {pageScope == null && n.page != null && (
              <span className="badge">Page {n.page}</span>
            )}
            <span>{n.created_at}</span>
            <div className="grow" />
            <button
              onClick={async () => {
                await deleteNote(index, n.id);
                load();
              }}
            >
              Delete
            </button>
          </div>
          <div className="body">{n.body}</div>
        </div>
      ))}
    </>
  );
}

function NoteForm({
  fixedPage,
  maxPage,
  onSubmit,
}: {
  fixedPage?: number;
  maxPage: number;
  onSubmit: (body: string, author: string, page: number | null) => Promise<void>;
}) {
  const [body, setBody] = useState("");
  // Remembered across notes, because the alternative is retyping your name for
  // every one and eventually not bothering — and a note with no author is a note
  // nobody can follow up on.
  const [author, setAuthor] = useState(() => localStorage.getItem("raglit.author") ?? "");
  const [page, setPage] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  return (
    <form
      className="noteform"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!body.trim()) return;
        setBusy(true);
        setErr("");
        try {
          localStorage.setItem("raglit.author", author.trim());
          const anchor = fixedPage ?? (page ? Number(page) : null);
          await onSubmit(body.trim(), author.trim(), anchor);
          setBody("");
          setPage("");
        } catch (e2: unknown) {
          setErr(String(e2));
        } finally {
          setBusy(false);
        }
      }}
    >
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder={
          fixedPage != null
            ? `A note about page ${fixedPage}: what a reader should know that the page does not show`
            : "A note about this document: what a reader should know that the document does not show"
        }
      />
      <div className="row">
        <input
          value={author}
          onChange={(e) => setAuthor(e.target.value)}
          placeholder="Your name"
          autoComplete="name"
        />
        {fixedPage != null ? (
          <span className="badge">Page {fixedPage}</span>
        ) : (
          <input
            type="number"
            min={1}
            max={maxPage || undefined}
            value={page}
            onChange={(e) => setPage(e.target.value)}
            placeholder="Page (optional)"
            style={{ width: 140 }}
          />
        )}
        <button className="primary" type="submit" disabled={busy || !body.trim()}>
          {busy ? "Saving…" : "Add note"}
        </button>
      </div>
      {err && <ErrorText raw={err} />}
    </form>
  );
}
