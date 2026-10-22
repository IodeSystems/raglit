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

## Slices

1. ◻ Pause: index_meta flag; the ingest worker, identity queue and re-embed
   skip a paused index; API, CLI and UI toggle.
2. ◻ Ignore at Enqueue: builtins + the index list, matched relative to the
   project root when the path is under it, otherwise on the absolute path.
   API and UI list editor.
3. ◻ Purge: preview and delete API, UI dialog; ignore-add offers a purge.

**next:** slice 1.

**risks:**
- Enqueue matching needs a relative path for `**/x/**` globs. A path outside
  every known root is matched as an absolute path, so a glob anchored at a
  root does not match it.
- A purge deletes data. The preview is the guard; nothing can be undone after
  the confirm.

**blocking decisions:** none open.

**optional extensions:**
- Pause the whole daemon (every index).
- Show ignored-at-enqueue counts in the UI.
