import { Outlet, useNavigate, useParams } from "@tanstack/react-router";
import Box from "@mui/material/Box";
import Chip from "@mui/material/Chip";
import MenuItem from "@mui/material/MenuItem";
import Select from "@mui/material/Select";

import { getJSON, listIndexes, type IndexInfo } from "../api";
import { usePoll } from "../usePoll";
import { AppShell, CrumbLink, ShellSearch } from "./AppShell";
import { indexSections } from "./NavDrawer";

// Everything below /i/:index — the app bar, the scope switcher, the search box
// and the nav drawer — rendered once and kept mounted while the routes
// underneath it change.
//
// This is the reason the tree is nested rather than flat. The search box lives
// HERE, so it holds its text and focus while you walk from a hit into a document
// and back out; a flat tree would remount it on every navigation and lose both.
//
// The frame itself is AppShell, shared with the overview and project pages, so
// the three scopes look like one application (F5, review walk 2026-10-20).
export function IndexShell() {
  const { index } = useParams({ from: "/i/$index" });
  const navigate = useNavigate();
  // Polled, not fetched once: a paused index says so on every page, and a
  // pause made elsewhere (the CLI, another tab) shows up here.
  const { data: listed } = usePoll<{ indexes: IndexInfo[] }>(listIndexes, 15000, []);
  const indexes = listed?.indexes ?? [];
  const paused = indexes.find((i) => i.name === index)?.paused;

  // The Problems entry carries a count so a problem is visible from every page
  // of the index. "withdrawn" is a decision, not a problem, so it is not
  // counted. Slow poll: problems change when an import finishes, not per second.
  const { data: probs } = usePoll<{ problems?: { kind: string }[] }>(
    () => getJSON("/api/problems", { index }),
    30000,
    [index],
  );
  const problems = (probs?.problems ?? []).filter((p) => p.kind !== "withdrawn").length;

  const { project } = splitName(index);

  // The index is in the URL, so switching it is a navigation, not a state
  // change. That is what makes an index linkable at all.
  const switcher = (
    <Select
      value={index}
      size="small"
      variant="outlined"
      aria-label="Index"
      onChange={(e) => void navigate({ to: "/i/$index", params: { index: e.target.value } })}
      sx={{
        minWidth: { xs: 0, sm: 180 },
        maxWidth: { xs: "45vw", sm: 320 },
        "& .MuiSelect-select": { py: 0.6, fontSize: 14 },
      }}
      renderValue={(v) => <ScopeLabel name={v} short />}
    >
      {/* The index from the URL is always an option, even if /indexes has not
          answered yet or does not know it. Otherwise a deep link to an index
          renders a picker showing some OTHER index while the page below it
          shows the right one. */}
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
  );

  return (
    <AppShell
      crumbs={[
        <CrumbLink key="projects" to="/">Projects</CrumbLink>,
        ...(project ? [<CrumbLink key="project" to={`/p/${project}`}>{project}</CrumbLink>] : []),
        switcher,
        ...(paused
          ? [
              <Chip
                key="paused"
                label="Paused"
                color="warning"
                size="small"
                onClick={() => void navigate({ to: "/i/$index/controls", params: { index } })}
              />,
            ]
          : []),
      ]}
      search={
        <ShellSearch
          placeholder="Search this index"
          onSearch={(q, nav) =>
            void nav({ to: "/i/$index/search", params: { index }, search: { q, mode: "bm25" } })
          }
        />
      }
      nav={indexSections(index, problems)}
    >
      <Outlet />
    </AppShell>
  );
}

// `bramble-v-quill__default` is a project and an index run together. Split the
// same way the daemon does (cmd/raglit/namespace.go: nsSep is "__"). An index
// with no separator has no project — it is not "a project called default", it
// is one nobody namespaced.
export function splitName(name: string): { project: string; local: string } {
  const sep = name.indexOf("__");
  return sep > 0
    ? { project: name.slice(0, sep), local: name.slice(sep + 2) }
    : { project: "", local: name };
}

// In the closed picker the project is already the crumb before it, so `short`
// shows only the index. The open list names both, because it lists every
// project's indexes.
function ScopeLabel({ name, short }: { name: string; short?: boolean }) {
  const { project, local } = splitName(name);
  return (
    <Box
      component="span"
      sx={{ display: "inline-flex", gap: 0.5, alignItems: "baseline", minWidth: 0 }}
    >
      {project && !short && (
        <Box component="span" sx={{ color: "text.secondary" }}>
          {project} /
        </Box>
      )}
      <Box
        component="span"
        sx={{ fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis" }}
      >
        {local}
      </Box>
    </Box>
  );
}
