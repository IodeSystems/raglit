# raglit: project-local homes + capability-aware model selection

Status: done 2026-07-23. Living doc — prune as follow-ups land.

## Goal (from user)

Run raglit per sub-project. `raglit init` inits in the local directory. raglit
should understand the OpenAI `/v1/models` catalog AND corrallm's capabilities
matrix, and filter model selection to what fits the role (embeddings, chat,
vision). Config lives in the cwd of init; init output includes MCP setup plus
ingest/query commands for reference.

## Delivered

- **Project-local home.** `raglit init` writes `./.raglit/` in the cwd (was the
  global `~/local/raglit`). Every other command resolves its home via
  `raglit.DiscoverHome()` — nearest ancestor `.raglit/` walking up from cwd
  (git-style), else `$RAGLIT_HOME`, else `~/local/raglit`. `--home` overrides.
  - `home.go`: `ProjectHomeName=".raglit"`, `DiscoverHome`, `findProjectHome`.
  - `main.go`/`doctor.go`/`pipeline.go`: default home → `DiscoverHome`.
  - `init.go`: default home → `Home(ProjectHomeName)`.
  - `.gitignore`: `/.raglit/`.

- **Capability-aware model selection.** corrallm's `/v1/models` enriches each
  entry with `type`/`capability`/`state`/`quality`/`modalities` (image ⇒
  vision). init parses the full catalog (`cmd/raglit/models.go`), and when the
  server reports capabilities it filters each pick list per role — image-in for
  vision, embeddings for embed — sorted ready-first then quality-desc, default =
  best. A plain OpenAI server (no enrichment) shows the full list, as before.
  Not corrallm's `/v1/capabilities` manifest: the per-model enrichment is a
  superset (it carries vision via modalities, which the manifest buckets don't).

- **Post-init output** (`printPostInit`): MCP stdio setup — a
  `claude mcp add-json raglit '{...}'` line and a `.mcp.json` block, both pinned
  to the project's absolute `.raglit` `--home` (works from any client cwd) — plus
  ingest/index/search/status/doctor reference commands.

## Verified

- Unit: `models_test.go` (filter/sort/label/hasCapabilities), `home_test.go`
  (walk-up discovery + fallback). Full suite green.
- Live: `init` against `https://llm.iodesystems.com/v1` filtered the 12-model
  catalog to 3 vision / 1 embed; wrote `./.raglit/config.json`; `doctor` from a
  nested subdir discovered the project home.

## Deferred / not done

- `raglit init` help still describes the wizard tersely; no `--yes`/non-interactive
  flag (piping answers works).
- corrallm `/v1/capabilities` manifest unused (per-model enrichment suffices).
- Chat-model selection not surfaced in init (only vision + embed are configured
  today); `roleMatches` already supports "chat" if a chat pick is added later.
