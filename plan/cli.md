# CLI — short help, watch DIR, index NAME, POSIX flags

How this plan works: living plan, see ~/CLAUDE.md "Planning". Status marks:
◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

USER 2026-10-23 (via dun-51 and directly): "the raglit --help is so damn
busy"; `raglit watch DIR --index NAME`; `raglit index --list` and `raglit
index NAME <cmd>`; POSIX flags: "I don't like single dash long flags. Single
dash is for combining and final set flag argument."

## Slices

1. ✅ `--shared a,b` on serve and search — 8942d88.
2. ✅ watch DIR --index NAME, index --list, index NAME subcommands, short root
   help (helptext.go table) — cff6cf6. With no ./.raglit, watch writes
   <daemon root>/projects/<project>/config.json, never the shared home.
3. ✅ B: a read may name one index of any project (`proj:idx`) — 87aaf12.
4. ✅ D: MCP ingest paths[], MCP forget — 9af06d1.
5. ◻ POSIX flags + cobra routing.
   - Plan: alias-import `flag "github.com/spf13/pflag"` across cmd/raglit
     (67 FlagSets, API mostly the same), plus a cobra root for routing,
     groups, help and command-name completion; each command keeps its
     FlagSet (DisableFlagParsing). helptext.go entries become Short/Long.
   - Single-letter flags need shorthands in pflag: `-n` in search, serve,
     daemon, similar, service logs. Custom flag.Value types need Type().
   - Breakage (measured 2026-10-23): the systemd unit generator writes
     `daemon -check -addr … -embed` (service.go, also service.go:191), and
     the installed unit uses them; the daemon's self-update re-execs os.Args.
     Fix the generator to `--` and reinstall the unit BEFORE the binary
     changes. yscr has one comment + one plan line with single-dash flags.
     Tests: providers_test.go, service_launchd_test.go. dun uses `--` only.
   - USER: `raglit doctor` detects a stale unit (single-dash flags, or
     different from what `service install` writes) and offers to rewrite it.

**next:** slice 5. Tell dun-51 when it lands.

**risks:**
- A config written by watch round-trips through Config: unknown keys drop,
  and `ocr`/`image_embed` zero blocks get written (equal to defaults).
- A test run on 2026-10-23 wrote `watch`/`indexes` into ~/local/raglit/
  config.json; those two keys were removed by hand. Tests now set
  RAGLIT_HOME.
- `index NAME pause|purge` print the namespaced name (proj__name).

**blocking decisions:** none open.

**optional extensions:** `--public` on watch / `index NAME public|private`
(needs OIDC; USER: later).
