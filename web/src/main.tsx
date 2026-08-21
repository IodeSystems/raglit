import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "@tanstack/react-router";
import CssBaseline from "@mui/material/CssBaseline";
import { ThemeProvider } from "@mui/material/styles";

import { applyLegacyHashRedirect } from "./legacyHash";
import { router } from "./router";
import { theme } from "./theme";
import "./styles.css";

// Before the router reads the URL, not after. A hash link from the old page has
// to be rewritten while it is still the URL — once "/" has redirected to the
// default index, the hash is gone and the link is silently a dashboard.
applyLegacyHashRedirect();

const el = document.getElementById("root");
if (!el) throw new Error("no #root");

createRoot(el).render(
  <StrictMode>
    {/* styles.css is still imported above and still styles the panes that have
        not been converted yet. It goes when the last one does — running both for
        a while is the price of converting a UI pane by pane instead of in one
        commit that cannot be reviewed. */}
    <ThemeProvider theme={theme} defaultMode="system">
      <CssBaseline />
      <RouterProvider router={router} />
    </ThemeProvider>
  </StrictMode>,
);
