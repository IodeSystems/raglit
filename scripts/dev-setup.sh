#!/usr/bin/env bash
# dev-setup.sh — put this checkout back into a working local-development state.
#
# What "working" means here: sibling repos are checked out, go.work points at
# them, and the pre-push guard is installed so none of that leaks into a push.
# Safe to re-run; every step is idempotent and reports what it did.
#
# Why go.work rather than a `replace` in go.mod: go.work is per-machine and
# gitignored, so the same tree builds against a local sibling for you and
# against the published module for CI. A replace in go.mod cannot do both, and
# the version it hides is the one everyone else actually gets.
#
#   ./scripts/dev-setup.sh            link every sibling this repo needs
#   ./scripts/dev-setup.sh --check    report only, change nothing (exit 1 if wrong)
set -euo pipefail

ORG_URL=${ORG_URL:-https://github.com/IodeSystems}
SIBLING_DIR=${SIBLING_DIR:-..}

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
check_only=0
[ "${1:-}" = "--check" ] && check_only=1

say()  { printf '  %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*" >&2; }

# self is this repo's own module path, read from go.mod rather than `go list` —
# go list needs the very module graph this script may be here to repair.
# Without excluding it the repo links against ITSELF, and go.work then refuses
# to load at all: "path ... appears multiple times in workspace".
self=$(awk '/^module /{print $2; exit}' go.mod)

# LINKED lists the sibling modules this repo develops against. Derived from the
# module paths the repo requires, so adding a dependency does not mean editing
# this script — but kept to iodesystems/* because those are the ones we build
# from source rather than consume as a release.
mapfile -t LINKED < <(
	go list -m -f '{{.Path}}' all 2>/dev/null |
		grep -E '^github\.com/iodesystems/' | grep -vxF "$self" | sort -u
) || true

if [ ${#LINKED[@]} -eq 0 ]; then
	# go list needs a resolvable module graph, which is the thing we may be here
	# to repair. Fall back to reading go.mod directly.
	mapfile -t LINKED < <(grep -oE 'github\.com/iodesystems/[a-z0-9_-]+' go.mod | grep -vxF "$self" | sort -u)
fi

echo "==> siblings ($SIBLING_DIR)"
missing=0
present=()
for mod in "${LINKED[@]}"; do
	name=${mod##*/}
	path="$SIBLING_DIR/$name"
	if [ -d "$path/.git" ]; then
		say "$name — present ($(git -C "$path" rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?'))"
		present+=("$path")
	elif [ "$check_only" = 1 ]; then
		warn "$name — MISSING (run without --check to clone)"
		missing=1
	else
		say "$name — cloning from $ORG_URL/$name.git"
		if git clone --quiet "$ORG_URL/$name.git" "$path"; then
			present+=("$path")
		else
			warn "$name — clone FAILED; skipping (it may not be published yet)"
			missing=1
		fi
	fi
done

echo "==> go.work"
want=$'go '"$(go mod edit -json | sed -n 's/.*"Go": "\([^"]*\)".*/\1/p' | head -1)"$'\n\nuse (\n\t.\n'
for p in "${present[@]}"; do want+=$'\t'"$p"$'\n'; done
want+=$')\n'

# Both sides through command substitution: it strips trailing newlines, and
# comparing a stripped file against an unstripped $want reports "stale" forever.
if [ -f go.work ] && [ "$(cat go.work)" = "$(printf '%s' "$want")" ]; then
	say "up to date ($((${#present[@]})) sibling(s))"
elif [ "$check_only" = 1 ]; then
	warn "go.work missing or stale — run without --check"
	missing=1
else
	printf '%s' "$want" >go.work
	say "wrote go.work with $((${#present[@]})) sibling(s)"
fi

# go.work is per-machine state. It must never be committed, or it becomes the
# same problem as a replace in go.mod, with the added charm of being invisible.
for f in go.work go.work.sum; do
	if ! grep -qxF "$f" .gitignore 2>/dev/null; then
		if [ "$check_only" = 1 ]; then
			warn ".gitignore missing $f"
			missing=1
		else
			echo "$f" >>.gitignore
			say "added $f to .gitignore"
		fi
	fi
done

echo "==> push guard"
hooks_path=$(git config --get core.hooksPath || true)
if [ "$hooks_path" = "scripts/hooks" ]; then
	say "core.hooksPath = scripts/hooks"
elif [ "$check_only" = 1 ]; then
	warn "core.hooksPath is '${hooks_path:-<default>}' — the pre-push guard is NOT active"
	missing=1
else
	git config core.hooksPath scripts/hooks
	say "core.hooksPath -> scripts/hooks"
fi
[ -x scripts/hooks/pre-push ] || { [ "$check_only" = 1 ] && { warn "scripts/hooks/pre-push not executable"; missing=1; } || chmod +x scripts/hooks/pre-push; }

echo "==> go.mod"
if leaked=$(grep -nE '=>[[:space:]]*(/|\.\.?/)' go.mod); then
	warn "go.mod still carries local replace directives:"
	printf '%s\n' "$leaked" | sed 's/^/      /' >&2
	warn "these are what the pre-push guard blocks; migrate them to go.work above"
	warn "or list the module in .replace-allow if it has no publishable version yet"
else
	say "clean — no local replace directives"
fi

echo "==> build"
if [ "$check_only" = 1 ]; then
	say "skipped (--check)"
elif go build ./... 2>&1 | head -5; then
	say "go build ./... ok"
fi

[ "$missing" = 0 ] || exit 1
