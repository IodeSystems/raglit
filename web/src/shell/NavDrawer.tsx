import { Link, useRouterState } from "@tanstack/react-router";
import Box from "@mui/material/Box";
import Chip from "@mui/material/Chip";
import Divider from "@mui/material/Divider";
import Drawer from "@mui/material/Drawer";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import ListSubheader from "@mui/material/ListSubheader";

export const DRAWER_WIDTH = 232;
export const APPBAR_HEIGHT = 56;

// The side navigation, for every scope: the daemon, a project, an index.
//
// One component for all three is the F5 fix (plan/ui-redesign.md, review walk
// 2026-10-20). The overview and project pages used to have a top bar with a
// centred page and the index pages a sidebar, so walking from a project into
// one of its indexes changed the whole frame. Now only the entries change.
//
// The index entries keep the grouping that replaced seven flat tabs:
//
//   CORPUS  what is in this index, and how do I find it
//   WORK    what is it doing, and what went wrong
//   INDEX   how this index is set up
//
// A drawer rather than tabs because the list keeps growing, and a tab bar that
// grows wraps, while a drawer scrolls.

export type NavItem = {
  label: string;
  to: string;
  // Active only on this exact path. Needed for a scope's root ("/i/:index",
  // "/p/:project", "/"), which is a PREFIX of every other entry — the trap
  // spa-ui.md already recorded on the tab bar, where every tab read as active.
  exact?: boolean;
  // A problem count, not a size. It renders as an alarm, so an entry with
  // nothing wrong must pass undefined or 0 — a grey zero next to "Problems"
  // reads as a badge that failed to load.
  count?: number;
  // A small mark in front of the label (the branch glyph).
  mark?: string;
  title?: string;
};

export type NavSection = { heading: string; items: NavItem[] };

export function indexSections(index: string, problems?: number): NavSection[] {
  const root = `/i/${index}`;
  return [
    {
      heading: "Corpus",
      items: [
        { label: "Overview", to: root, exact: true },
        { label: "Documents", to: `${root}/d` },
        { label: "Search", to: `${root}/search` },
        { label: "Types & fields", to: `${root}/types` },
        { label: "Review", to: `${root}/attest` },
      ],
    },
    {
      heading: "Work",
      items: [
        { label: "Activity", to: `${root}/activity` },
        { label: "Problems", to: `${root}/health`, count: problems },
        { label: "Import jobs", to: `${root}/jobs` },
      ],
    },
    {
      heading: "Index",
      items: [
        { label: "Controls", to: `${root}/controls` },
        { label: "Branches", to: `${root}/branches` },
      ],
    },
  ];
}

// Daemon-wide settings. Listed under the overview's sections and on the
// settings pages themselves.
export const settingsSection: NavSection = {
  heading: "Settings",
  items: [{ label: "Models", to: "/settings/models" }],
};

export function settingsSections(): NavSection[] {
  return [{ heading: "", items: [{ label: "All projects", to: "/", exact: true }] }, settingsSection];
}

// `open` matters only at phone width, where the drawer leaves the flow and
// slides in over the page (styles.css: .sidebar / .sidebar-open). On a desktop
// it is always shown and the class does nothing.
export function NavDrawer({ sections, open }: { sections: NavSection[]; open: boolean }) {
  return (
    <Drawer
      variant="permanent"
      className={"sidebar" + (open ? " sidebar-open" : "")}
      sx={{
        width: DRAWER_WIDTH,
        flexShrink: 0,
        [`& .MuiDrawer-paper`]: {
          width: DRAWER_WIDTH,
          boxSizing: "border-box",
          borderRight: 1,
          borderColor: "divider",
          // Below the app bar rather than over it: the bar carries the scope
          // switcher, which belongs to the whole window and not to this scope.
          top: APPBAR_HEIGHT,
          height: `calc(100% - ${APPBAR_HEIGHT}px)`,
        },
      }}
    >
      <Box component="nav" aria-label="Sections" sx={{ overflowY: "auto", py: 0.5 }}>
        {sections
          .filter((s) => s.items.length)
          .map((s, i) => (
            <Box key={s.heading || i}>
              {i > 0 && <Divider sx={{ my: 0.5 }} />}
              <List
                dense
                disablePadding
                subheader={
                  s.heading ? (
                    <ListSubheader
                      disableSticky
                      sx={{
                        bgcolor: "transparent",
                        lineHeight: "28px",
                        fontSize: 11,
                        letterSpacing: ".6px",
                        textTransform: "uppercase",
                      }}
                    >
                      {s.heading}
                    </ListSubheader>
                  ) : undefined
                }
              >
                {s.items.map((it) => (
                  <NavEntry key={it.to} item={it} />
                ))}
              </List>
            </Box>
          ))}
      </Box>
    </Drawer>
  );
}

function NavEntry({ item }: { item: NavItem }) {
  // Active state is derived from the real location rather than from Link's
  // activeProps, so the exact/prefix rule is one line and visible here.
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const active = item.exact
    ? pathname === item.to || pathname === item.to + "/"
    : pathname === item.to || pathname.startsWith(item.to + "/");

  return (
    <ListItemButton
      component={Link}
      to={item.to}
      selected={active}
      title={item.title}
      sx={{ py: 0.4, pl: 2, borderRadius: 0 }}
    >
      {item.mark && (
        <Box component="span" sx={{ color: "text.secondary", mr: 0.75 }} aria-hidden>
          {item.mark}
        </Box>
      )}
      <ListItemText
        primary={item.label}
        slotProps={{
          primary: {
            sx: {
              fontSize: 14,
              fontWeight: active ? 600 : 400,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            },
          },
        }}
      />
      {!!item.count && item.count > 0 && (
        <Chip
          label={item.count.toLocaleString()}
          size="small"
          sx={{
            height: 18,
            fontSize: 11,
            bgcolor: (t) => t.palette.roles.err,
            color: "#fff",
            "& .MuiChip-label": { px: 0.75 },
          }}
        />
      )}
    </ListItemButton>
  );
}
