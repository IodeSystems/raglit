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

// The index workspace navigation.
//
// It replaces seven flat tabs — Dashboard, Health, Ingest jobs, Documents,
// Search, Branches, Review — that put the corpus, the machinery and the workflow
// at one level in no particular order. The grouping here is the claim that they
// are three different questions:
//
//   CORPUS  what is in this index, and how do I find it
//   WORK    what is it doing, and what went wrong
//   INDEX   what is this index, and how is it configured
//
// A drawer rather than tabs because the list is going to keep growing — four of
// these seven entries had no UI at all a day ago — and a tab bar that grows
// wraps, while a drawer scrolls.

type Item = {
  label: string;
  to: string;
  // count is a problem count, not a size. It renders as an alarm, so a pane with
  // nothing wrong must pass undefined rather than 0 — a grey zero next to
  // "Problems" reads as a badge that failed to load.
  count?: number;
  // isNew marks a surface the daemon has served for a while and the UI has not.
  // Dropped once the redesign lands; it exists so the gap is visible while it is
  // being closed.
  isNew?: boolean;
};

type Section = { heading: string; items: Item[] };

export function NavDrawer({
  index,
  problems,
  activity,
}: {
  index: string;
  problems?: number;
  activity?: number;
}) {
  const sections: Section[] = [
    {
      heading: "Corpus",
      items: [
        { label: "Documents", to: `/i/${index}/d` },
        { label: "Search", to: `/i/${index}/search` },
        { label: "Types & fields", to: `/i/${index}/types`, isNew: true },
      ],
    },
    {
      heading: "Work",
      items: [
        { label: "Activity", to: `/i/${index}/activity`, count: activity, isNew: true },
        { label: "Problems", to: `/i/${index}/health`, count: problems },
        { label: "Ingest jobs", to: `/i/${index}/jobs` },
      ],
    },
    {
      heading: "Index",
      items: [
        { label: "Overview", to: `/i/${index}` },
        { label: "Branches", to: `/i/${index}/branches` },
        { label: "Review", to: `/i/${index}/attest` },
      ],
    },
  ];

  return (
    <Drawer
      variant="permanent"
      sx={{
        width: DRAWER_WIDTH,
        flexShrink: 0,
        [`& .MuiDrawer-paper`]: {
          width: DRAWER_WIDTH,
          boxSizing: "border-box",
          borderRight: 1,
          borderColor: "divider",
          // Below the app bar rather than over it: the bar carries the scope
          // switcher, which belongs to the whole window and not to this index.
          top: 56,
          height: "calc(100% - 56px)",
        },
      }}
    >
      <Box sx={{ overflowY: "auto", py: 0.5 }}>
        {sections.map((s, i) => (
          <Box key={s.heading}>
            {i > 0 && <Divider sx={{ my: 0.5 }} />}
            <List
              dense
              disablePadding
              subheader={
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
              }
            >
              {s.items.map((it) => (
                <NavItem key={it.to} item={it} index={index} />
              ))}
            </List>
          </Box>
        ))}
      </Box>
    </Drawer>
  );
}

function NavItem({ item, index }: { item: Item; index: string }) {
  // Active state is derived from the real location rather than from Link's
  // activeProps, because "/i/:index" is a PREFIX of every other entry here — the
  // trap spa-ui.md already recorded on the tab bar, where every tab read as
  // active until `exact` was added to the dashboard link.
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const root = `/i/${index}`;
  const active = item.to === root ? pathname === root : pathname.startsWith(item.to);

  return (
    <ListItemButton
      component={Link}
      to={item.to}
      selected={active}
      sx={{ py: 0.4, pl: 2, borderRadius: 0 }}
    >
      <ListItemText
        primary={item.label}
        slotProps={{ primary: { sx: { fontSize: 14, fontWeight: active ? 600 : 400 } } }}
      />
      {item.count !== undefined && item.count > 0 && (
        <Chip
          label={item.count}
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
      {item.isNew && item.count === undefined && (
        <Box
          component="span"
          sx={{ width: 6, height: 6, borderRadius: "50%", bgcolor: (t) => t.palette.roles.run }}
          title="new surface"
        />
      )}
    </ListItemButton>
  );
}
