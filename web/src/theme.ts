import { createTheme } from "@mui/material/styles";

// The theme. Colour VALUES live in the token block at the top of styles.css;
// this file maps them onto MUI's palette roles.
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

// Each role is a CSS variable defined in styles.css (the token block), not a hex
// value. Fixed 2026-10-20: a pane reading `t.palette.roles.chip` in an sx
// callback gets the DEFAULT (light) scheme's value whatever scheme is showing —
// MUI only swaps `t.vars.*` per scheme — so the Types & fields command block
// was a light chip under light dark-mode text. A var() reference resolves at
// paint time in whichever scheme is active, so every existing caller is right.
const role = {
  ok: "var(--ok)",
  warn: "var(--warn)",
  err: "var(--err)",
  run: "var(--run)",
  vision: "var(--vision)",
  textEngine: "var(--text-eng)",
  chip: "var(--chip)",
};
const roles = { light: role, dark: role };

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
