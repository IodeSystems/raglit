import { Outlet } from "@tanstack/react-router";

// The outermost frame. Everything an index-scoped route renders lands in the
// Outlet below.
//
// Deliberately thin: the index picker, the search box and the tab bar all live
// in IndexShell, because they need an index and this route does not have one.
// The legacy-hash redirect that used to live here moved to legacyHash.ts, where
// it runs before the router exists — see the comment there for the race it lost.
export function RootShell() {
  return <Outlet />;
}
