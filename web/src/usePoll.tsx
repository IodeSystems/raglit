import { useEffect, useRef, useState } from "react";

// Fetch now, then again every `ms`, until the component goes away.
//
// Two properties the obvious setInterval does not have. It does not clear the
// data between polls — a dashboard that blanks to "loading…" every three seconds
// is unreadable — and it drops the result of a request that lands after the
// dependencies changed, so switching index does not get the old index's numbers
// written over the new one's a moment later.
export function usePoll<T>(
  fetcher: () => Promise<T>,
  ms: number,
  deps: unknown[],
): { data: T | null; error: string; refresh: () => void } {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  useEffect(() => {
    let live = true;
    setData(null);
    const run = () => {
      fetcherRef
        .current()
        .then((d) => {
          if (!live) return;
          setData(d);
          setError("");
        })
        .catch((e: unknown) => live && setError(String(e)));
    };
    run();
    const id = setInterval(run, ms);
    return () => {
      live = false;
      clearInterval(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, ms, tick]);

  return { data, error, refresh: () => setTick((t) => t + 1) };
}
