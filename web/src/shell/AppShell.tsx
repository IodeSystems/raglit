import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { Fragment, useEffect, useState, type ReactNode } from "react";
import AppBar from "@mui/material/AppBar";
import Box from "@mui/material/Box";
import InputBase from "@mui/material/InputBase";
import Paper from "@mui/material/Paper";
import Toolbar from "@mui/material/Toolbar";
import Typography from "@mui/material/Typography";

import { APPBAR_HEIGHT, NavDrawer, type NavSection } from "./NavDrawer";
import "./AppShell.css";

// The one frame every page sits in: the daemon overview, a project, an index.
//
//   app bar   menu button (phone only) · raglit · breadcrumb · search
//   sidebar   the scope's sections (NavDrawer)
//   main      the page, starting next to the sidebar
//
// Before 2026-10-20 there were two frames — a top bar with a centred page for
// projects, a sidebar for indexes — and a ~200px dead gutter between the
// sidebar and the content, because <main> was offset by the drawer width while
// the permanent drawer already took that width in the flex row. Here the
// drawer takes its width once and <main> is just the rest of the row.
export function AppShell({
  crumbs,
  search,
  nav,
  children,
}: {
  crumbs: ReactNode[];
  search?: ReactNode;
  nav: NavSection[];
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  // A tap on a sidebar entry navigates; the drawer must not stay over the page
  // it just opened.
  useEffect(() => setOpen(false), [pathname]);

  return (
    <Box sx={{ display: "flex" }}>
      <AppBar
        position="fixed"
        elevation={0}
        color="default"
        sx={{ borderBottom: 1, borderColor: "divider", bgcolor: "background.paper" }}
      >
        <Toolbar
          variant="dense"
          sx={{ minHeight: APPBAR_HEIGHT, height: APPBAR_HEIGHT, gap: 1.5, px: { xs: 2, sm: 2 } }}
        >
          <button
            type="button"
            className="menu-toggle"
            aria-label={open ? "Close menu" : "Open menu"}
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
          >
            ☰
          </button>
          <Typography
            component={Link}
            to="/"
            sx={{
              display: { xs: "none", sm: "block" },
              fontWeight: 650,
              fontSize: 15,
              letterSpacing: ".2px",
              textDecoration: "none",
              color: "text.primary",
              flexShrink: 0,
            }}
          >
            raglit
          </Typography>
          <nav className="appcrumbs" aria-label="Breadcrumb">
            {crumbs.map((c, i) => (
              <Fragment key={i}>
                {i > 0 && (
                  <span className="appcrumbs-sep" aria-hidden>
                    /
                  </span>
                )}
                <span className={"appcrumbs-item" + (i === crumbs.length - 1 ? " last" : "")}>
                  {c}
                </span>
              </Fragment>
            ))}
          </nav>
          {search}
          <Box sx={{ flex: 1, display: { xs: "none", md: "block" } }} />
        </Toolbar>
      </AppBar>

      <NavDrawer sections={nav} open={open} />
      {open && <div className="sidebar-scrim" onClick={() => setOpen(false)} />}

      <Box component="main" className="app-main" sx={{ mt: `${APPBAR_HEIGHT}px` }}>
        <div className="page">{children}</div>
      </Box>
    </Box>
  );
}

// A crumb that links. The last crumb is usually plain text (or the index
// switcher), so this is for the ones before it.
export function CrumbLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link to={to} className="appcrumbs-link">
      {children}
    </Link>
  );
}

// The search box in the app bar. Submitting navigates, so a search is a link
// and the back button walks searches. Seeded from the URL so a pasted
// /search?q=… shows its query in the box.
export function ShellSearch({
  placeholder,
  onSearch,
}: {
  placeholder: string;
  onSearch: (q: string, navigate: ReturnType<typeof useNavigate>) => void;
}) {
  const navigate = useNavigate();
  const urlQ = useRouterState({
    select: (s) => (s.location.search as { q?: string }).q ?? "",
  });
  const [q, setQ] = useState(urlQ);
  useEffect(() => setQ(urlQ), [urlQ]);

  return (
    <Paper
      component="form"
      role="search"
      variant="outlined"
      onSubmit={(e) => {
        e.preventDefault();
        onSearch(q.trim(), navigate);
      }}
      sx={{
        display: "flex",
        alignItems: "center",
        px: 1,
        flex: { xs: "1 1 auto", md: "1 1 420px" },
        minWidth: 0,
        maxWidth: 560,
        bgcolor: "transparent",
      }}
    >
      <InputBase
        type="search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder={placeholder}
        autoComplete="off"
        inputProps={{ "aria-label": placeholder }}
        sx={{ flex: 1, minWidth: 0, fontSize: 14 }}
      />
    </Paper>
  );
}
