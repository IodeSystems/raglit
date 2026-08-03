import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "@tanstack/react-router";

import { applyLegacyHashRedirect } from "./legacyHash";
import { router } from "./router";
import "./styles.css";

// Before the router reads the URL, not after. A hash link from the old page has
// to be rewritten while it is still the URL — once "/" has redirected to the
// default index, the hash is gone and the link is silently a dashboard.
applyLegacyHashRedirect();

const el = document.getElementById("root");
if (!el) throw new Error("no #root");

createRoot(el).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
