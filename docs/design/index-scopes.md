# Index scopes — search picks indexes by directory

How this plan works: living plan, see ~/CLAUDE.md "Planning". Status marks:
◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

USER 2026-10-23: "indexes would have a list of scopes for directory patterns
they apply in, if they don't, then they are global"; "search uses scopes and
optionally names. global indexes can be opt in or ambient." Decisions
(AskUserQuestion 2026-10-23): names REPLACE the scope set; scopes live ON THE
INDEX (daemon-side meta); existing proj__* indexes are auto-scoped to their
project dir.

## Model

- Index meta `scopes`: JSON list of directory patterns. A plain absolute dir
  matches itself and everything under it; a pattern with `*`/`?` is a glob
  (sources.go syntax) matched against the directory.
- No scopes → global. Meta `ambient`: "1" = every search reads it; else
  opt-in (read only when named).
- Meta `scopes` absent (never set) = opt-in global, and the startup migration
  sets it once.
- Search with no names: indexes whose scopes match the client's cwd, plus
  ambient globals, filtered by the ACL. Wire: selector token `@cwd:<dir>`,
  resolved by the daemon (selectIndexes).
- Names given (`--index a,b`, MCP `index`) → exactly those.
- dun compatibility: an EXPLICIT `--project P` with no names keeps the old
  meaning (P's indexes + shared). Config-derived project → scope mode.
- Reads need no project any more; writes still do.
- A bare name with no project resolves to the unique `*__name` index.

## Slices

1. ✅ Store: Scopes/SetScopes/Ambient/SetAmbient, ScopeMatches; Registry
   ScopedFor(ctx, dir).
2. ✅ Daemon: `@cwd:` + bare-name tokens in selectIndexes; idxRow scopes and
   ambient; POST /api/index/scope; startup migration (project dir from
   watches.json, else the index's doc root unless it is /, $HOME or above
   $HOME, else opt-in global).
3. ✅ Ingest claims a scope: the client sends its project dir; an index whose
   scopes were never set takes it.
4. ✅ Client: search CLI and MCP serve send `@cwd:`; `raglit index list` lists
   every index (scope or global mode, ✓ when it applies here);
   `raglit index NAME scope add|rm PATTERN`, `raglit index NAME global
   ambient|opt-in`.

**next:** watch the live daemon's migration log (journalctl --user -u raglit
| grep scoped) and correct wrong guesses with `raglit index NAME scope`.
Tell dun-a8: no change for `--project P` callers.

Live-tested 2026-10-23 on a scratch daemon (127.0.0.1:7499): ingest claim,
scope reads, search with no project, ambient/opt-in, `proj:idx` with no
project, MCP search in scope mode, MCP ingest refused with no project.

**risks:**
- MCP list_indexes in scope mode returns every index unfiltered (with
  scopes); with OIDC on the daemon still filters by ACL.
- A bare name with no project that two projects share matches nothing; the
  error says to write proj:idx only in `raglit index NAME`, not in search.
- Config `shared` is not consulted in scope mode; a shared index must carry
  its own scope or be ambient.
- Migration by doc root guesses: an index fed from several projects gets
  their common parent.

**blocking decisions:** none open.

**optional extensions:** embedded mode (`--embedded`) ignores scopes.
