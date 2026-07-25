#!/usr/bin/env bash
# Claude Code UserPromptSubmit hook: inject raglit search hits for the user's
# prompt as ambient context, so retrieval happens automatically every turn
# (instead of the model having to call the search tool itself).
#
# Install: see examples/hooks/README.md.
#
# Optional env knobs:
#   RAGLIT_INDEX   restrict to an index (or comma-separated set); empty = all
#                  (this is the ONLY scoping raglit search supports today — there
#                   is no per-path/subdirectory filter; see README).
#   RAGLIT_MODE    bm25 (default) | vec | hybrid  (vec/hybrid need an --embed'd index)
#   RAGLIT_N       max hits to inject (default 5)
#   RAGLIT_BIN     path to the raglit binary (default: `raglit` on PATH)
set -euo pipefail

bin=${RAGLIT_BIN:-raglit}
mode=${RAGLIT_MODE:-bm25}
n=${RAGLIT_N:-5}

input=$(cat)  # hook event JSON on stdin

# The prompt field is `user_input` in current Claude Code; older builds used
# `prompt`. Accept either so the hook is robust across versions.
prompt=$(printf '%s' "$input" | jq -r '.user_input // .prompt // ""')

# Skip trivial and slash-command prompts to avoid noise + latency.
[ "${#prompt}" -ge 12 ] || exit 0
case "$prompt" in /*) exit 0 ;; esac

# Build args: mode + limit, plus an optional index scope.
args=(search --mode "$mode" -n "$n")
[ -n "${RAGLIT_INDEX:-}" ] && args+=(--index "$RAGLIT_INDEX")

# Query raglit (routes to the shared daemon by default). Never fail the prompt on
# a retrieval error — just inject nothing.
hits=$("$bin" "${args[@]}" "$prompt" 2>/dev/null || true)
[ -n "$hits" ] && [ "$hits" != "(no matches)" ] || exit 0

# Exit 0 + plain stdout → added to Claude's context for this turn.
printf 'Relevant indexed context (raglit):\n%s\n' "$hits"
