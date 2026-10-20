import { Outlet } from "@tanstack/react-router";

// The outermost frame. Everything an index-scoped route renders lands in the
// Outlet below.
//
// Deliberately thin: the frame (app bar, breadcrumb, sidebar) is AppShell, and
// each scope renders it with its own entries — the overview page, ProjectShell
// and IndexShell — because the sidebar and the search box depend on the scope.
// The legacy-hash redirect that used to live here moved to legacyHash.ts, where
// it runs before the router exists — see the comment there for the race it lost.
export function RootShell() {
  return <Outlet />;
}
