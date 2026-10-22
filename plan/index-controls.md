# Index controls — pause, ignore patterns, purge by pattern

How this plan works: living plan, see ~/CLAUDE.md "Planning". Status marks:
◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

USER 2026-10-22: "we desperately need a pause index, and the ability to purge
file patterns and ignore file patterns. I'm burning money on node_modules."

## Incident (2026-10-22)

- `dun` sessions ran `raglit ingest <files>` on their workspace, including
  `.claude/worktrees/*/mobile/node_modules/**`. Result: about 7,400 ingest jobs
  and about 1,900 caption jobs on `dun__dun-main`.
- raglit applies its built-in ignores (`node_modules`, dot-dirs) only in
  `sync` and the watcher (PlanSources). `ingest <files>`, POST /ingest and the
  MCP ingest tool enqueue whatever they are given.
- Stopped by hand: pending jobs on those paths set to `skipped` (1,307 caption,
  578 ingest). dun fixed its side in dun 37a01f0.
- About 1,900 documents from those paths are still in the index. Slice 3
  purges them.

## Decisions (USER 2026-10-22)

- **Pause:** a paused index runs nothing (ingest, caption, embed, re-embed).
  New jobs still queue and run on resume. Stored in the index, so it survives
  restarts. UI toggle plus `raglit index pause|resume`.
- **Ignore:** a per-index glob list, editable in the UI and stored in the
  index. Enforced at Enqueue, which every ingest path passes through: builtins
  + config + the index list.
- **Purge:** enter a glob, preview the count and sample paths, confirm. Deletes
  the documents with their fragments, vectors and captions, plus pending jobs.
  Adding an ignore pattern offers "also purge N matching docs".

## Slices (✅ 2026-10-22)

1. ✅ Pause — bac640c, bed21a0, cb6d426. The flag is in index_meta. Ingest
   claims, caption claims and re-embed skip a paused index. Also
   POST /api/index/pause, `raglit pause|resume`, and a header chip.
2. ✅ Ignore at Enqueue — 07225f5. `**/node_modules/**` applies to every index,
   plus each index's own list, matched against the absolute path. A refused
   path returns ErrIgnored, so a batch skips it and counts it.
3. ✅ Purge — 46ac360, cf7d793. Dry-run preview first. When an ignore pattern
   is added, the page offers to purge what already matches.

The Controls page was checked in the browser against a scratch daemon: pause,
the chip, adding an ignore pattern, the purge offer, purge and resume. The CLI
was checked too.

**next:** deploy, then purge `**/.claude/worktrees/**` from `dun__dun-main`
(about 1,900 documents).

**risks:**
- A job already running when you pause finishes. Only new claims stop.
- Re-embed runs only at daemon start. Vectors missing on a paused index fill
  on the next start after it is resumed.
- Patterns match the absolute path. A glob anchored at a root (`mobile/**`)
  matches nothing; use `**/mobile/**`.
- A purge cannot be undone. The preview is the only guard.
- A direct `raglit ingest <dir>` walk applies only `node_modules` and the
  index's own list. For build output (Android, iOS, Python) see the blocking
  decision.

**blocking decisions:**
- Should more build-output directories be ignored at Enqueue on every index
  (`.gradle`, `Pods`, `android/build`, `ios/build`, `.expo`)? Asked
  2026-10-22.

**optional extensions:**
- Pause the whole daemon (every index).
- Show ignored-at-enqueue counts in the UI.
