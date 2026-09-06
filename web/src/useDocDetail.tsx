import { createContext, useCallback, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";

import { docDetail, type DocDetail } from "./api";

// One fetch per document, shared by every sub-tab.
//
// The alternative — each sub-tab fetching what it needs — is four requests for
// an answer /api/doc-detail already assembles in one, and four chances for the
// tabs to disagree about what the document is. `reload` exists because writes
// (a re-title, a note, a correction) change what the OTHER tabs should say.

export function useDocDetail(index: string, doc: string) {
  const [detail, setDetail] = useState<DocDetail | null>(null);
  const [error, setError] = useState<string>("");

  const load = useCallback(() => {
    let live = true;
    setError("");
    docDetail(index, doc)
      .then((d) => live && setDetail(d))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [index, doc]);

  useEffect(() => {
    // Clear first. Without this, walking from one document to another renders
    // the PREVIOUS document's pages under the new document's title until the
    // fetch lands — which is worse than a spinner, because it looks like data.
    setDetail(null);
    return load();
  }, [load]);

  return { detail, error, reload: load };
}

type DocCtx = { index: string; doc: string; detail: DocDetail; reload: () => void };

const Ctx = createContext<DocCtx | null>(null);

export function DocProvider({ value, children }: { value: DocCtx; children: ReactNode }) {
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

// Throws rather than returning null: every consumer renders inside DocShell's
// Outlet, so a null here is a wiring mistake, and a component quietly rendering
// "no data" would hide it.
export function useDoc(): DocCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error("useDoc outside DocShell");
  return v;
}
