import { useState } from "react";

import { identify, type DocIdentity } from "../api";
import { ErrorText } from "../ui/Details";

// The kinds identity.go accepts, by family (captionprofile.go). CLOSED
// vocabularies — NormalizeKind rejects anything outside them — so this is a
// select, not a text box. Typing a kind the server will refuse and finding out
// on submit is a worse way to learn the list.
//
// Kept in step with the profiles in captionprofile.go. A kind added there and
// not here is simply missing from the picker; a kind here and not there is a 400.
const KIND_GROUPS: { family: string; label: string; kinds: string[] }[] = [
  {
    family: "legal",
    label: "Legal and records",
    kinds: ["deed", "survey", "agreement", "correspondence", "court-filing", "certification", "analysis", "notes", "commercial", "other"],
  },
  {
    family: "code",
    label: "Code",
    kinds: ["entrypoint", "library", "api", "ui", "schema", "test", "generated", "config", "build", "script", "other"],
  },
  {
    family: "docs",
    label: "Project docs",
    kinds: ["readme", "plan", "design", "spec", "guide", "reference", "changelog", "notes", "other"],
  },
  { family: "image", label: "Images", kinds: ["photo", "screenshot", "diagram", "map", "scan", "other"] },
];

// Re-title a document: say what it IS, as a person.
//
// The case this exists for: a document was auto-titled as if it were the survey,
// when it is somebody's annotation OF the survey — the machine read the sheet
// and described the sheet, and the thing that made the document worth keeping
// went unmentioned. Nobody could fix that, so the corpus kept lying.
//
// It posts to /api/identify, which records Source='person'. identity.go:443
// makes that binding: a re-run may fill blanks but must not overwrite what a
// person said. Without that guarantee this button would be a suggestion the next
// captioning pass silently discards.
export function Retitle({
  index,
  doc,
  current,
  onDone,
}: {
  index: string;
  doc: string;
  current?: DocIdentity;
  onDone: () => void;
}) {
  const [name, setName] = useState(current?.name ?? "");
  const [summary, setSummary] = useState(current?.summary ?? "");
  const [kind, setKind] = useState(current?.kind ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await identify(index, doc, { name: name.trim(), summary: summary.trim(), kind });
      onDone();
    } catch (e2: unknown) {
      setErr(String(e2));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="noteform"
      onSubmit={(e) => void save(e)}
    >
      <div className="row">
        <input
          style={{ flex: 1, minWidth: 0 }}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Title: what this document is, not what the file is called"
          aria-label="Title"
        />
        <select value={kind} onChange={(e) => setKind(e.target.value)} aria-label="Kind">
          <option value="">Kind not set</option>
          {KIND_GROUPS.map((g) => (
            <optgroup key={g.family} label={g.label}>
              {g.kinds.map((k) => (
                <option key={k} value={`${g.family}:${k}`}>
                  {k.replace(/-/g, " ")}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
      </div>
      <textarea
        value={summary}
        onChange={(e) => setSummary(e.target.value)}
        placeholder="Summary: what it says, and why it matters"
        aria-label="Summary"
      />
      {/* The server enforces a floor on the summary (identity.go rejects "A
          legal document." as an answer that establishes nothing). Saying so here
          beats a 400 with the number in it. */}
      <div className="row">
        <button className="primary" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Save title"}
        </button>
        <span className="muted">
          Write the summary as at least one full sentence. A title you save here is kept when the document is read again.
        </span>
      </div>
      {err && <ErrorText raw={err} />}
    </form>
  );
}
