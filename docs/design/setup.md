# Setup and capabilities — install like poly-lsp

How this plan works: living plan per ~/CLAUDE.md "Planning" — current state and
active slices only; finished slices move to plan/done/, deferred ones to
docs/design/icebox.md.

USER 2026-10-24 (via dun): "can raglit be as pleasant to install as poly-lsp?
With a few configuration parameters, like optional embedding/image/summary
provider/model, with a note of its capability and what would be needed to make
it more useful." All 5 slices approved: "I like all 5 though, so stage it."

Already true (verified 2026-10-24): pure Go (`CGO_ENABLED=0 go build
./cmd/raglit` builds); one binary is CLI + daemon + MCP serve; with no models,
BM25 search over text/markdown/code works; `serve --embedded` needs no daemon;
providers + a provider/model per role exist in config.json and through
`/api/settings/models`.

## Slices

1. ✅ **Key by reference.** Provider `api_key_env` / `api_key_file`, read at each
   call (`Provider.Key`). Settings API saves, keeps and shows them.
   - risks: a daemon under systemd does not see a shell's variables; only
     `api_key_file` or the unit's environment reaches it. Legacy top-level
     `api_key` has no reference form (RAGLIT_LLM_KEY covers it).
2. ✅ **identity_provider.** The caption/summary role on its own provider;
   role "identity" in `/api/settings/models` and a "Summary" card in the UI.
   A save that omits the role keeps it.
3. ✅ **`raglit doctor --json`.** {binary, build, platform, home, configured,
   daemon, capabilities:[{name, on, via|needs+how}], roles:[{role, provider,
   model, set, hasKey, fallsBackTo, adds}], extractors:[{name, found, path,
   adds, optional, install:{cmd, needs}}], extractorsSeenBy, update}. camelCase
   like poly-lsp. `update` is poly-lsp's updatecheck.go ported verbatim in
   contract (origin release|go install|source, checkedAt, 2 s, 24 h cache,
   silent offline, modified checkout not checked); `-tags release` marks a
   release build (release_on.go). tesseract joined the probed tools.
4. ✅ **MCP setup notification.** notifications/message {level, logger
   "raglit/setup", data: {capabilities, roles, extractors, extractorsSeenBy,
   missing}} once per session on the client's notifications/initialized.
   level "warning" only when a core capability (keyword, semantic, PDF text,
   scans) is off, else "info". Proxy mode reports the DAEMON's summary
   (`GET /api/setup`, admin-only with auth on); doctor --json uses it too.
5. ✅ **Releases.** `.github/workflows/release.yml`: test (CGO_ENABLED=0,
   with the extractors apt-installed), then one Linux runner builds the UI
   and cross-builds linux/darwin × amd64/arm64 with `-trimpath -tags
   release`, smoke-tests linux_amd64 (`--help`, `doctor --json --local`), and
   on a `v*` tag publishes the four tarballs + SHA256SUMS. workflow_dispatch
   is a dry run. 2026-10-24: dry run green on GitHub (run 40130473952, at
   b8cf7b8): tests pass in CI, tarballs hold raglit + LICENSE +
   THIRD_PARTY_NOTICES.md + README.md, the binary serves the built UI, and
   the stamp is clean (the first run was stamped dirty by the UI build; fixed
   in b8cf7b8, and the smoke step now fails on it).
   - v0.1.0 released 2026-10-24 (tag on a32c5db): 4 tarballs + SHA256SUMS.
     The downloaded linux_amd64 asset passes its checksum, is stamped v0.1.0
     clean, and doctor reports update {origin release, latest v0.1.0}.
   - risks: macOS binaries are cross-built and never run.

6. ✅ **License conformance.** `tools/notices` (`make notices`): every Go
   module the binary links (GOWORK=off) and every npm production package must
   be MIT, BSD-2/3, Apache-2.0, ISC, 0BSD, Unlicense, CC0 or public domain;
   unrecognised license text fails until it is read and listed in `reviewed`.
   Writes THIRD_PARTY_NOTICES.md (license + NOTICE texts); the release
   workflow runs it and packs it into every tarball. 2026-10-24: 206
   dependencies, all allowed. LICENSE (MIT, Iode Systems) added to raglit,
   poly-lsp-mcp, mcpshell and dun.
   - risks: a module's top-level license files only; vendored code under
     another license inside a module is not seen.
   - optional extensions: the same tool in poly-lsp-mcp (tree-sitter
     grammars) and dun (mobile/ npm).

## Findings

- `go install …/cmd/raglit@ver` embeds the PLACEHOLDER web UI: web/dist is a
  committed two-file stub, and the real UI is built by `make stamped|release`
  (needs node). A release archive must be built with the UI; plain go install
  gives CLI + MCP + API, no UI.

- raglit's gitignored go.work makes local builds link other versions than a
  release (mcp-go v1.3.0 local, v1.0.0 released); local tests do not cover the
  released set.

## Assumptions

- dun writes providers and roles through `PUT /api/settings/models` when a
  daemon is up, else config.json directly.
