import { useSyncExternalStore } from "react";

// Operator tools: whether the UI shows what only the person running raglit
// needs — re-index and re-read, import jobs, controls, branches, types and
// fields, model settings, passage counts.
//
// Off by default. The focus groups of 2026-10-24 walked the UI as a support
// engineer and an office manager, and every one of those controls read to them
// as "a developer's tool": words they could not act on, beside the document
// they came to read. The operator walk passed with the same pages, so the
// controls stay, behind one switch in the app bar.
//
// A view preference, NOT access control. The daemon decides what a principal
// may do; this only decides what is on the screen. Kept per browser, in
// localStorage, and every reader of it re-renders when it changes — in this tab
// through the listeners, in another tab through the storage event.

const KEY = "raglit.operator";
const listeners = new Set<() => void>();
// The value when storage refuses writes (a private window), so the switch
// still flips for this page.
let fallback = false;

function read(): boolean {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export function setOperator(on: boolean) {
  try {
    if (on) localStorage.setItem(KEY, "1");
    else localStorage.removeItem(KEY);
  } catch {
    fallback = on;
  }
  listeners.forEach((l) => l());
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  const onStorage = (e: StorageEvent) => e.key === KEY && l();
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(l);
    window.removeEventListener("storage", onStorage);
  };
}

export function useOperator(): boolean {
  return useSyncExternalStore(subscribe, () => read() || fallback, () => false);
}
