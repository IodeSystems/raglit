import { listDocuments } from "./api";

// Old document URLs carry the ABSOLUTE path ("/home/<user>/…/x.pdf"): every
// link written before 2026-10-20, and every legacy hash link (legacyHash.ts).
// The daemon still accepts that path, but the decision (plan/ui-redesign.md,
// review walk 2026-10-20) is that the absolute path never appears in a URL. So
// the router asks here for the display path and redirects to it.
//
// The display path comes from the index's own document list (row.rel), not
// from cutting the root off the path: a document in raglit's storage is
// spelled "@attachments/…", which no string operation on the root produces.
// The root is the fallback for a document the list does not hold (yet).

export function isAbsoluteDocPath(doc: string): boolean {
  return doc.startsWith("/") || doc.startsWith("file://") || doc.startsWith("~/");
}

type Lookup = { root?: string; rel: Map<string, string> };
const cache = new Map<string, Promise<Lookup>>();

function lookup(index: string): Promise<Lookup> {
  let p = cache.get(index);
  if (!p) {
    p = listDocuments(index).then((got) => ({
      root: got.root,
      rel: new Map((got.documents ?? []).filter((d) => d.rel).map((d) => [d.path, d.rel])),
    }));
    // A failed lookup is not cached: the next navigation tries again.
    p.catch(() => cache.delete(index));
    cache.set(index, p);
  }
  return p;
}

// relDocPath returns the display path for an absolute document path, or null
// when it cannot be found (the caller then keeps the URL it has).
export async function relDocPath(index: string, doc: string): Promise<string | null> {
  const abs = doc.replace(/^file:\/\//, "");
  try {
    const { root, rel } = await lookup(index);
    const hit = rel.get(abs) ?? rel.get(doc);
    if (hit) return hit;
    if (root) {
      const r = root.replace(/\/+$/, "");
      if (abs.startsWith(r + "/")) return abs.slice(r.length + 1);
    }
  } catch {
    // No list, no rewrite. The daemon still resolves the absolute path.
  }
  return null;
}
