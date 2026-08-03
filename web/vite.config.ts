import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The daemon serves this bundle from a Go binary, so the build targets that and
// nothing else.
//
// `dist` is committed and //go:embed'd — see plan/spa-ui.md. Two consequences
// shape this config: asset names must not contain characters go:embed skips (a
// leading `_` or `.` makes a file invisible to the embed, and the page then 404s
// for an asset that is plainly on disk), and there is no CDN or separate origin,
// so everything is same-origin relative to "/".
//
// The proxy is for `npm run dev` only. It points at a daemon you are already
// running, so the dev server can serve the page while the real API answers the
// fetches — the alternative is mocking a surface of forty operations.
const DAEMON = process.env.RAGLIT_DAEMON || "http://127.0.0.1:7777";

// The prefixes the daemon owns. Kept in step with the deny-list in
// cmd/raglit/webui.go: a path this proxy forwards but that catch-all swallows
// (or the reverse) is a route that works in dev and 404s in the binary.
const API_PREFIXES = [
  "/api",
  "/status",
  "/indexes",
  "/search",
  "/search-figures",
  "/ingest",
  "/openapi.json",
  "/docs",
  "/attest",
];

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // Named without a hash prefix that go:embed would skip. Hashes stay in the
    // middle of the filename, where they still bust a browser cache.
    rollupOptions: {
      output: {
        entryFileNames: "assets/app-[hash].js",
        chunkFileNames: "assets/chunk-[hash].js",
        assetFileNames: "assets/asset-[hash][extname]",
      },
    },
  },
  server: {
    proxy: Object.fromEntries(
      API_PREFIXES.map((p) => [p, { target: DAEMON, changeOrigin: true }]),
    ),
  },
});
