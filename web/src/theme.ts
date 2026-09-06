import { createTheme } from "@mui/material/styles";

// The theme, and the only place colour is decided.
//
// The palette is carried over from the sheet this replaces (styles.css, itself
// ported verbatim from the old vanilla page) — not out of sentiment, but because
// the semantic roles were already right and already meant something to a reader
// of this tool: ok/warn/err for state, `run` for work in flight, `vision` for a
// model-backed lane, `generated` for words a machine wrote. Those distinctions
// are the product; the hand-named classes around them were not.
//
// `colorSchemes` rather than a media query in CSS, so a component can ask the
// theme what the current scheme is instead of every pane re-deriving it.

const roles = {
  light: {
    ok: "#16a34a",
    warn: "#d97706",
    err: "#dc2626",
    run: "#2563eb",
    vision: "#7c3aed",
    textEngine: "#0891b2",
    chip: "#eef2f7",
  },
  dark: {
    ok: "#22c55e",
    warn: "#f59e0b",
    err: "#ef4444",
    run: "#3b82f6",
    vision: "#a78bfa",
    textEngine: "#22d3ee",
    chip: "#20242d",
  },
};

declare module "@mui/material/styles" {
  interface Palette {
    roles: typeof roles.light;
  }
  interface PaletteOptions {
    roles?: typeof roles.light;
  }
}

export const theme = createTheme({
  cssVariables: { colorSchemeSelector: "class" },
  colorSchemes: {
    light: {
      palette: {
        mode: "light",
        background: { default: "#f6f7f9", paper: "#ffffff" },
        text: { primary: "#1a1d21", secondary: "#6b7280" },
        primary: { main: "#2563eb" },
        divider: "#e5e7eb",
        roles: roles.light,
      },
    },
    dark: {
      palette: {
        mode: "dark",
        background: { default: "#0f1115", paper: "#171a21" },
        text: { primary: "#e6e8eb", secondary: "#9aa4b2" },
        primary: { main: "#3b82f6" },
        divider: "#262b34",
        roles: roles.dark,
      },
    },
  },
  shape: { borderRadius: 10 },
  typography: {
    fontFamily: `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`,
    fontSize: 14,
    // A section heading in this tool is a label, not a headline. The old sheet
    // said the same thing in a rule on h2; saying it here means a pane cannot
    // opt out by accident.
    overline: { fontSize: 11, letterSpacing: ".5px", fontWeight: 600 },
  },
  components: {
    // Dense by default. This is a tool for looking at thousands of rows, and
    // MUI's comfortable defaults put roughly half as many on a screen.
    MuiTable: { defaultProps: { size: "small" } },
    MuiTextField: { defaultProps: { size: "small" } },
    MuiButton: { defaultProps: { size: "small" }, styleOverrides: { root: { textTransform: "none" } } },
    MuiChip: { defaultProps: { size: "small" } },
    MuiTableCell: { styleOverrides: { root: { verticalAlign: "top" } } },
    MuiPaper: { defaultProps: { elevation: 0 }, styleOverrides: { root: { backgroundImage: "none" } } },
  },
});

// mono is the face for anything that is a path, an id, or a verbatim string
// from the machine. Exported rather than repeated: it was written out in six
// places in the sheet this replaces.
export const mono = `ui-monospace, SFMono-Regular, Menlo, monospace`;
