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
| `RAGLIT_PATH`  | (all)   | restrict to a path subtree (prefix; see below)      |
| `RAGLIT_MODE`  | `bm25`  | `bm25` \| `vec` \| `hybrid` (vec/hybrid need embeddings) |
| `RAGLIT_N`     | `5`     | max hits injected                                   |
| `RAGLIT_BIN`   | `raglit`| path to the raglit binary                           |

Set them in the hook's `command` if you want per-project behavior, e.g.
`"command": "RAGLIT_INDEX=docs RAGLIT_PATH=/repo/src/api/ ~/.claude/hooks/raglit-context.sh"`.

## Scoping: index and/or path subtree

Two independent scopes:

- **By index** — `--index` / `RAGLIT_INDEX` selects one index or a comma-separated
  set. Good when subtrees map to separate indexes (`raglit sync`'s `indexes` map
  with per-index `roots` is built for this).
- **By path subtree** — `--path` / `RAGLIT_PATH` constrains results to documents
  whose stored path **starts with** the given prefix, across all search modes
  (bm25 / vec / hybrid, and `search_figures`). Pass a trailing `/` for a clean
  directory subtree, e.g. `--path /repo/src/api/`. The prefix must match the path
  form stored in the index (absolute file path, or a `file://` URL) — check
  `raglit list_documents` / `list_documents` output if unsure.

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
