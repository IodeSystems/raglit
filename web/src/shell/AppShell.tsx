import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { Fragment, useState, type ReactNode } from "react";
import AppBar from "@mui/material/AppBar";
import Badge from "@mui/material/Badge";
import Box from "@mui/material/Box";
import FormControlLabel from "@mui/material/FormControlLabel";
import IconButton from "@mui/material/IconButton";
import InputBase from "@mui/material/InputBase";
import Paper from "@mui/material/Paper";
import Switch from "@mui/material/Switch";
import Toolbar from "@mui/material/Toolbar";
import Typography from "@mui/material/Typography";
import SearchIcon from "@mui/icons-material/Search";
import TuneIcon from "@mui/icons-material/Tune";

import { APPBAR_HEIGHT, NavDrawer, type NavSection } from "./NavDrawer";
import { useResetOn } from "../useResetOn";
import { IndexPicker, inParam, searchable } from "../ui/IndexPicker";
import { setOperator, useOperator } from "../ui/operator";
import { useIndexInfo } from "../panes/IndexRows";
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
  useResetOn([pathname], () => setOpen(false));

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
          <OperatorSwitch />
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

// The switch for operator tools (ui/operator.ts). In the app bar because it
// changes every page, and because the person who needs it is the one who will
// look for it there.
function OperatorSwitch() {
  const operator = useOperator();
  return (
    <FormControlLabel
      sx={{ ml: 0, mr: 0, flexShrink: 0, "& .MuiFormControlLabel-label": { fontSize: 13, color: "text.secondary" } }}
      control={
        <Switch
          size="small"
          checked={operator}
          onChange={(e) => setOperator(e.target.checked)}
          slotProps={{ input: { "aria-label": "Operator tools" } }}
        />
      }
      label={<Box component="span" sx={{ display: { xs: "none", sm: "inline" } }}>Operator tools</Box>}
      title="Show the controls for running raglit: re-indexing, import jobs, branches, settings"
    />
  );
}

// The search box in the app bar. Submitting navigates, so a search is a link
// and the back button walks searches. Seeded from the URL so a pasted
// /search?q=… shows its query in the box.
//
// The gear chooses which indexes the search reads, before searching (the
// results page offers the same after). `pool` is the selector of the indexes
// this scope may search ("" is every index); `defaults` is what it searches
// when nobody chose ("" or absent is the whole pool). The choice travels as
// `in`, a comma list, so it is part of the link too.
export function ShellSearch({
  placeholder,
  onSearch,
  pool = "",
  defaults,
}: {
  placeholder: string;
  onSearch: (q: string, navigate: ReturnType<typeof useNavigate>, within: string) => void;
  pool?: string;
  defaults?: string[];
}) {
  const navigate = useNavigate();
  const url = useRouterState({
    select: (s) => s.location.search as { q?: string; in?: string },
  });
  const urlQ = url.q ?? "";
  const urlIn = url.in ?? "";
  const [q, setQ] = useState(urlQ);
  const [within, setWithin] = useState(urlIn);
  const [picking, setPicking] = useState(false);
  useResetOn([urlQ], () => setQ(urlQ));
  useResetOn([urlIn], () => setWithin(urlIn));

  const info = useIndexInfo();
  const candidates = [...info.values()].filter((i) => inPool(i.name, pool) && searchable(i));
  const base = defaults ?? candidates.map((c) => c.name);
  const selected = within ? within.split(",") : base;

  return (
    <Paper
      component="form"
      role="search"
      variant="outlined"
      onSubmit={(e) => {
        e.preventDefault();
        onSearch(q.trim(), navigate, within);
      }}
      sx={{
        display: "flex",
        alignItems: "center",
        pl: 1,
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
      <IconButton
        size="small"
        aria-label="Choose indexes to search"
        title={`Searching ${selected.length} of ${candidates.length || selected.length} indexes. Choose which.`}
        onClick={() => setPicking(true)}
      >
        <Badge color="primary" variant="dot" invisible={!within}>
          <TuneIcon fontSize="small" />
        </Badge>
      </IconButton>
      <IconButton size="small" type="submit" aria-label="Search" title="Search">
        <SearchIcon fontSize="small" />
      </IconButton>
      <IndexPicker
        open={picking}
        candidates={candidates}
        selected={selected}
        onClose={() => setPicking(false)}
        onApply={(names) => {
          const next = inParam(names, base);
          setWithin(next);
          setPicking(false);
          // With a query typed, the choice is acted on at once; with none, it
          // waits for the search it was made for.
          if (q.trim()) onSearch(q.trim(), navigate, next);
        }}
      />
    </Paper>
  );
}

// inPool applies the daemon's index selector — a comma list of names and
// "prefix*" wildcards, "" for every index — to one index name.
export function inPool(name: string, pool: string): boolean {
  if (!pool) return true;
  return pool.split(",").some((t) => {
    const s = t.trim();
    return s.endsWith("*") ? name.startsWith(s.slice(0, -1)) : name === s;
  });
}
