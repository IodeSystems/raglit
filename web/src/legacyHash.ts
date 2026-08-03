// Links people already sent each other.
//
// The page this replaces kept its routes in the HASH, and those URLs are in
// somebody's chat history. Redirecting them costs a few lines; not redirecting
// them means every link already sent lands on the dashboard with no sign it was
// pointing somewhere.
//
//   #/documents/<index>/<path>[/<sub>]  →  /i/<index>/d/<path>/<sub>
//   #/jobs/<id>                         →  /i/<index>/jobs/<id>
//   #/<tab>                             →  /i/<index>/<tab>
//
// This runs BEFORE the router is created, not from an effect inside it. As an
// effect it lost a race it could not win: "/" redirects to the default index in
// beforeLoad, so by the time a component mounted the hash was gone and the
// legacy link silently became the dashboard. Rewriting the URL before anything
// reads it has no race to lose.
export function applyLegacyHashRedirect(): void {
  if (!location.hash.startsWith("#/")) return;
  const to = pathForLegacyHash(location.hash);
  if (to) history.replaceState(null, "", to);
}

// Exported separately so the mapping can be checked against the cases the old
// parser handled. The old scheme is frozen — it is whatever is already written
// down — so this is pinnable rather than guesswork.
export function pathForLegacyHash(hash: string, fallbackIndex = "default"): string | null {
  const parts = hash
    .replace(/^#\/?/, "")
    .split("/")
    .filter(Boolean)
    .map(decodeURIComponent);
  if (!parts.length) return null;
  const tab = parts[0]!;

  if (tab === "jobs") {
    const id = parts[1];
    return `/i/${enc(fallbackIndex)}/jobs${id ? "/" + enc(id) : ""}`;
  }

  if (tab !== "documents") {
    if (!["dashboard", "health", "search"].includes(tab)) return null;
    // The old dashboard was a tab; the new one is the index root.
    return `/i/${enc(fallbackIndex)}${tab === "dashboard" ? "" : "/" + tab}`;
  }

  if (parts.length < 3) return `/i/${enc(parts[1] ?? fallbackIndex)}/d`;

  // documents / <index> / <path> [/ <sub>], with the repairs the old parser did:
  // a link whose path got split across segments is rejoined rather than 404'd,
  // and one that lost its leading slash gets it back. Both are hand-typed or
  // older links that still point at a real document.
  const subs = ["pages", "transcript", "seen", "attest", "history"];
  const rest = parts.slice(2);
  let sub = "pages";
  if (rest.length > 1 && subs.includes(rest[rest.length - 1]!)) sub = rest.pop()!;
  let doc = rest.join("/");
  if (doc && !doc.startsWith("/") && rest.length > 1) doc = "/" + doc;
  if (!doc) return `/i/${enc(parts[1]!)}/d`;
  // "attest" was a document sub-tab and is an index-level route now, so it maps
  // to the asset view rather than to a sub-tab that no longer exists.
  if (sub === "attest") return `/i/${enc(parts[1]!)}/attest/a/${enc(doc)}`;
  return `/i/${enc(parts[1]!)}/d/${enc(doc)}/${sub}`;
}

const enc = encodeURIComponent;
