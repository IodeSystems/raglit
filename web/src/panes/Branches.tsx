import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";

import { listBranches, postJSON, type Branch } from "../api";
import { usePoll } from "../usePoll";
import { Details, ErrorText } from "../ui/Details";

// The branches of one index.
//
// A branch is a copy-on-write overlay: reads see the branch's own documents
// first and fall through to the parent for everything it has not changed, and a
// deletion is a tombstone that hides the parent's copy rather than removing it.
// So its document count is what it CHANGED, and it is not comparable with the
// parent's.
//
// The daemon has had fork/list/delete since the branch-storage work landed and
// nothing has ever shown them, which is why they are all empty: the feature was
// reachable only by curl.
export function Branches() {
  const { index } = useParams({ from: "/i/$index" });
  const { data, error, refresh } = usePoll<{ branches?: Branch[] }>(listBranches, 5000, []);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const mine = (data?.branches ?? []).filter((b) => b.parent === index);

  // The branch inherits its parent's project namespace.
  //
  // Measured, not assumed: forking `uitest` off `dun__dun` with the name sent
  // raw created an index literally called `uitest`, which is not in the `dun`
  // project at all — it landed in the unnamespaced group beside `default`,
  // orphaned from the corpus it overlays. A branch belongs to the same project
  // as the index it branches from; nothing else makes sense, and the daemon has
  // no opinion because a branch name is just an index name to it.
  const sep = index.indexOf("__");
  const ns = sep > 0 ? index.slice(0, sep + 2) : "";
  const branchName = (want: string) => (want.startsWith(ns) ? want : ns + want);
  const local = sep > 0 ? index.slice(sep + 2) : index;

  const fork = async (e: React.FormEvent) => {
    e.preventDefault();
    const want = name.trim();
    if (!want) return;
    setBusy(true);
    setErr("");
    try {
      await postJSON("/api/branches", undefined, { name: branchName(want), parent: index });
      setName("");
      refresh();
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  const drop = async (b: Branch) => {
    // Deleting a branch destroys its own rows — the edits that are only there.
    // The parent is untouched, and saying so is what makes this answerable.
    if (
      !confirm(
        `Delete branch ${b.name}?\n\nIts ${changes(b.documents ?? 0)} are lost. ` +
          `${b.parent} is untouched.`,
      )
    )
      return;
    setBusy(true);
    setErr("");
    try {
      const r = await fetch("/api/branches?" + new URLSearchParams({ name: b.name }), {
        method: "DELETE",
      });
      if (!r.ok) throw new Error(await r.text());
      refresh();
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="probgroup panel">
        <h3>
          Branches of {local} ({mine.length})
        </h3>
        <p className="why">
          A branch is a working copy of this index. It sees every document here
          and keeps only the documents it changes, so its count is changes, not
          contents. Deleting a branch leaves this index as it is.
        </p>
        {error && <ErrorText raw={error} />}
        {!error && !mine.length && (
          <p className="why">Nothing branches from this index.</p>
        )}
        {mine.map((b) => (
          <div className="prob" key={b.name}>
            <div className="subj">
              <span className="branchmark">⑂</span>
              <Link to="/i/$index" params={{ index: b.name }}>
                {b.name}
              </Link>
              <span className="badge" style={{ marginLeft: ".4rem" }}>
                {changes(b.documents ?? 0)}
              </span>
            </div>
            <div className="det">
              {[
                b.created_at ? `created ${when(b.created_at)}` : null,
                b.last_accessed_at ? `last used ${when(b.last_accessed_at)}` : null,
              ]
                .filter(Boolean)
                .join(" · ")}
            </div>
            <div className="acts">
              <button className="act" disabled={busy} onClick={() => drop(b)}>
                Delete
              </button>
            </div>
          </div>
        ))}
      </div>

      <div className="probgroup panel">
        <h3>Create a branch</h3>
        <p className="why">
          Some servers do not allow branches; if so, creating one is refused and
          the reason is shown here.
        </p>
        <Details>
          Branches need a scoped daemon (one started without <code>--home</code>).
        </Details>
        <form className="acts" onSubmit={fork}>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="New branch name"
            autoComplete="off"
          />
          <button className="act" disabled={busy || !name.trim()}>
            Create from {local}
          </button>
          {/* Show the name it will actually get. The namespace is not
              decoration — it is what keeps the branch inside its project. */}
          {name.trim() && ns && (
            <span className="muted small">→ {branchName(name.trim())}</span>
          )}
        </form>
        {err && <ErrorText raw={err} />}
      </div>
    </>
  );
}

// Branch timestamps are seconds-or-nanos depending on nothing observable, so
// treat a small number as seconds. A branch forked today rendering as 1970 is
// the kind of detail that makes a whole view look untrustworthy.
function when(t: number): string {
  const ms = t > 1e15 ? t / 1e6 : t > 1e12 ? t : t * 1000;
  return new Date(ms).toLocaleString();
}

function changes(n: number): string {
  return `${n.toLocaleString()} ${n === 1 ? "changed document" : "changed documents"}`;
}
