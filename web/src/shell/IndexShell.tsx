import { Link, Outlet, useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import AppBar from "@mui/material/AppBar";
import Box from "@mui/material/Box";
import InputBase from "@mui/material/InputBase";
import MenuItem from "@mui/material/MenuItem";
import Paper from "@mui/material/Paper";
import Select from "@mui/material/Select";
import Toolbar from "@mui/material/Toolbar";
import Typography from "@mui/material/Typography";

import { listIndexes, type IndexInfo } from "../api";
import { DRAWER_WIDTH, NavDrawer } from "./NavDrawer";

// Everything below /i/:index — the app bar, the scope switcher, the search box
// and the nav drawer — rendered once and kept mounted while the routes
// underneath it change.
//
// This is the reason the tree is nested rather than flat. The search box lives
// HERE, so it holds its text and focus while you walk from a hit into a document
// and back out; a flat tree would remount it on every navigation and lose both.
//
// What changed 2026-08-19: the header used to carry a wordmark, a four-part
// breadcrumb AND a raw <select> holding the full index name — the same fact in
// two idioms, side by side, with the search box wrapping onto its own row
// underneath at anything narrower than a desktop. One switcher now says where
// you are and changes it; see plan/ui-redesign.md §5.
export function IndexShell() {
  const { index } = useParams({ from: "/i/$index" });
  const navigate = useNavigate();
  const [indexes, setIndexes] = useState<IndexInfo[]>([]);

  useEffect(() => {
    listIndexes()
      .then((got) => setIndexes(got.indexes ?? []))
      .catch(() => setIndexes([]));
  }, []);

  // The index is in the URL, so switching it is a navigation, not a state
  // change. That is what makes an index linkable at all.
  const switchIndex = (name: string) => {
    navigate({ to: "/i/$index", params: { index: name } });
  };

  return (
    <Box sx={{ display: "flex" }}>
      <AppBar
        position="fixed"
        elevation={0}
        color="default"
        sx={{ borderBottom: 1, borderColor: "divider", bgcolor: "background.paper" }}
      >
        <Toolbar variant="dense" sx={{ minHeight: 56, gap: 1.5 }}>
          <Typography
            component={Link}
            to="/"
            sx={{
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

          {/* One control, not a breadcrumb AND a picker. It names the scope and
              changes it, which is the only thing the pair did between them. */}
          <Select
            value={index}
            size="small"
            variant="outlined"
            onChange={(e) => switchIndex(e.target.value)}
            sx={{ minWidth: 240, "& .MuiSelect-select": { py: 0.6, fontSize: 14 } }}
            renderValue={(v) => <ScopeLabel name={v} />}
          >
            {/* The index from the URL is always an option, even if /indexes has
                not answered yet or does not know it. Otherwise a deep link to an
                index renders a picker showing some OTHER index while the page
                below it shows the right one. */}
            {!indexes.some((i) => i.name === index) && (
              <MenuItem value={index}>
                <ScopeLabel name={index} />
              </MenuItem>
            )}
            {indexes.map((i) => (
              <MenuItem key={i.name} value={i.name}>
                <ScopeLabel name={i.name} />
              </MenuItem>
            ))}
          </Select>

          <ShellSearch index={index} />
          <Box sx={{ flex: 1 }} />
        </Toolbar>
      </AppBar>

      <NavDrawer index={index} />

      <Box
        component="main"
        sx={{ flexGrow: 1, minWidth: 0, mt: "56px", ml: `${DRAWER_WIDTH}px`, p: 2 }}
      >
        <Outlet />
      </Box>
    </Box>
  );
}

// `delano-v-mckinnon__default` is a project and an index run together. Split the
// same way the daemon does (cmd/raglit/namespace.go: nsSep is "__"). An index
// with no separator has no project — it is not "a project called default", it is
// one nobody namespaced — so only the index shows.
function ScopeLabel({ name }: { name: string }) {
  const sep = name.indexOf("__");
  const project = sep > 0 ? name.slice(0, sep) : "";
  const local = sep > 0 ? name.slice(sep + 2) : name;
  return (
    <Box component="span" sx={{ display: "inline-flex", gap: 0.5, alignItems: "baseline" }}>
      {project && (
        <>
          <Box component="span" sx={{ color: "text.secondary" }}>
            {project}
          </Box>
          <Box component="span" sx={{ color: "text.secondary" }}>
            /
          </Box>
        </>
      )}
      <Box component="span" sx={{ fontWeight: 600 }}>
        {local}
      </Box>
    </Box>
  );
}

// The shell search box. Submitting navigates to the search route with the query
// in the URL — so a search is a link, and the back button walks searches.
function ShellSearch({ index }: { index: string }) {
  const navigate = useNavigate();
  // Seeded from the URL so landing on a pasted /search?q=… shows that query in
  // the box rather than an empty one next to its own results.
  const urlQ = useRouterState({
    select: (s) => (s.location.search as { q?: string }).q ?? "",
  });
  const [q, setQ] = useState(urlQ);
  useEffect(() => setQ(urlQ), [urlQ]);

  return (
    <Paper
      component="form"
      variant="outlined"
      onSubmit={(e) => {
        e.preventDefault();
        navigate({
          to: "/i/$index/search",
          params: { index },
          search: { q: q.trim(), mode: "bm25" },
        });
      }}
      sx={{ display: "flex", alignItems: "center", px: 1, flex: 1, maxWidth: 560, bgcolor: "transparent" }}
    >
      <InputBase
        type="search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="search this index — words from the document, not a question"
        autoComplete="off"
        sx={{ flex: 1, fontSize: 14 }}
      />
    </Paper>
  );
}
