# Ambient raglit retrieval via a Claude Code hook

`raglit-context.sh` is a Claude Code **`UserPromptSubmit`** hook: it runs before
Claude reads each prompt, queries raglit, and prints the top hits to stdout —
which Claude Code injects as extra context for that turn. The effect is *ambient*
retrieval: relevant indexed material shows up automatically, without the model
deciding to call the `search` tool.

(raglit is also an MCP server, so the model can call `search` / `get_document` /
`search_figures` itself. The hook is the opposite posture: automatic every turn.)

## Install

1. Copy the script somewhere stable and make it executable:

   ```sh
   cp examples/hooks/raglit-context.sh ~/.claude/hooks/raglit-context.sh
   chmod +x ~/.claude/hooks/raglit-context.sh
   ```

2. Register it in `~/.claude/settings.json` (user-wide) — or a project's
   `.claude/settings.json`, which **overrides** the user file (hooks don't merge):

   ```json
   {
     "hooks": {
       "UserPromptSubmit": [
         {
           "hooks": [
             { "type": "command", "command": "~/.claude/hooks/raglit-context.sh", "timeout": 10 }
           ]
         }
       ]
     }
   }
   ```

3. Needs `jq` and `raglit` on `PATH` (or set `RAGLIT_BIN` to an absolute path —
   hooks run with a minimal environment).

## Tuning (env vars)

| var            | default | meaning                                             |
|----------------|---------|-----------------------------------------------------|
| `RAGLIT_INDEX` | (all)   | restrict to an index / comma-separated set          |
| `RAGLIT_MODE`  | `bm25`  | `bm25` \| `vec` \| `hybrid` (vec/hybrid need embeddings) |
| `RAGLIT_N`     | `5`     | max hits injected                                   |
| `RAGLIT_BIN`   | `raglit`| path to the raglit binary                           |

Set them in the hook's `command` if you want per-project behavior, e.g.
`"command": "RAGLIT_INDEX=docs RAGLIT_MODE=hybrid ~/.claude/hooks/raglit-context.sh"`.

## Scoping: index, not path

raglit search scopes by **index** (`--index`), not by path or subdirectory. There
is no per-directory filter inside an index today (`WHERE fragments_fts MATCH ?`,
no path clause). So for hierarchical corpora, the way to constrain retrieval to a
subtree is to **give that subtree its own index** and point `RAGLIT_INDEX` at it.
`raglit sync` config (the `indexes` map with per-index `roots`) is built for
exactly this — one project can define several indexes over different directory
roots. See the top-level README's config section.

## Contract notes (verified against the current Claude Code hooks docs)

- **`UserPromptSubmit` takes no `matcher`** — it always fires.
- **stdin** is the event JSON; the prompt text is `user_input` (older builds:
  `prompt`). The script accepts either.
- **Exit 0 + stdout** → added to Claude's context. `timeout` is in **seconds**
  (default 30 if omitted).
- **Exit 2** blocks the prompt and shows stderr to *you*, not Claude. Any other
  non-zero is non-blocking (first stderr line lands in the transcript; the prompt
  proceeds). This script only ever exits 0, so it can never block a prompt.
- **Structured output** alternative (exit 0 with JSON): a top-level
  `{"additionalContext": "...", "suppressOutput": true}` instead of plain stdout.

## Cost

Fires on every substantive prompt → adds latency and tokens. The length/slash
guards, `RAGLIT_N`, and the `timeout` keep it bounded. Tighten to taste.
