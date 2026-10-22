import { useState } from "react";

// Run `reset` during render when any of `deps` changes, compared with Object.is
// like an effect's dependency list.
//
// The obvious alternative, resetting in an effect, renders one frame with the
// OLD state under the new key first: the previous document's pages under the
// new document's title. Setting state during render discards that frame (React
// re-renders before painting), so stale data never reaches the screen.
// https://react.dev/learn/you-might-not-need-an-effect#adjusting-some-state-when-a-prop-changes
export function useResetOn(deps: unknown[], reset: () => void): void {
  const [seen, setSeen] = useState(deps);
  if (deps.length !== seen.length || deps.some((d, i) => !Object.is(d, seen[i]))) {
    setSeen(deps);
    reset();
  }
}
