# Setup and capabilities — install like poly-lsp

How this plan works: living plan per ~/CLAUDE.md "Planning" — current state and
active slices only; finished slices move to plan/done/, deferred ones to
plan/icebox.md.

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
3. ◻ **`raglit doctor --json`.** {capabilities:[{name, on, needs, how}],
   roles:[{role, provider, model, set}], extractors:[{name, found, adds,
   install:{cmd, needs}}], update:{origin, current, latest, available, how,
   note, checked_at}}.
   - risks: `latest` needs a release source; until slice 5 it is unknown for
     every origin but "source".
4. ◻ **MCP setup notification.** The same summary as a logging notification,
   logger "raglit/setup", after `serve` starts.
5. ◻ **Releases.** `raglit_{os}_{arch}.tar.gz` + SHA256SUMS on GitHub releases.
   - blocking decision: Carl owns tagging and publishing; the workflow can be
     written and tested locally, a release is his call.

## Findings

- `go install …/cmd/raglit@ver` embeds the PLACEHOLDER web UI: web/dist is a
  committed two-file stub, and the real UI is built by `make stamped|release`
  (needs node). A release archive must be built with the UI; plain go install
  gives CLI + MCP + API, no UI.

## Assumptions

- dun writes providers and roles through `PUT /api/settings/models` when a
  daemon is up, else config.json directly.
