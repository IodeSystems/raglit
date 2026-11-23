# Test plan — pre-commit hook: internal/db matches `make generate`

## Block 1 — hook is sane and wired into dev-setup.sh

The hook exists, is executable, passes `sh -n`, and `scripts/dev-setup.sh`
chmods it alongside the pre-push guard.

```sh
produces=hook-sanity.log inputs=scripts/hooks/pre-commit,scripts/dev-setup.sh
set -e
chmod +x scripts/hooks/pre-commit
{
  test -x scripts/hooks/pre-commit
  sh -n scripts/hooks/pre-commit
  grep -n 'scripts/hooks/pre-commit' scripts/dev-setup.sh
} > "$OUT/hook-sanity.log" 2>&1
echo OK
```

## Block 2 — end-to-end: corrupt internal/db → hook blocks; clean tree → hook passes

Deterministic test of the hook's core decision with `make generate` STUBBED on
PATH so the sibling checkouts are not needed. The scratch repo is nested one
level under a temp dir and a fake `sqlc` directory is placed at its parent so
the hook's sibling check passes. A stub `make` script restores a clean
`internal/db` tree, simulating the real fork regenerating the correct files.

The stub writes ONLY into the scratch repo (`cd "$R"` first; the plan's cwd is
the project checkout, and an un-cd'd stub would destroy the project's own
`internal/`). It copies the *contents* of `clean-internal/db` into the repo's
`internal/db` — `cp -r` into an existing destination creates a subdirectory of
the same name, which would leave `internal/db/db/`.

No `set -e`: the hook correctly exits 1 on the corrupt case, and the test
must survive that to assert on the rc.

```sh
produces=hook-stub-e2e.log inputs=scripts/hooks/pre-commit
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
# Truncate the artifact: it is appended per run, so without this the log
# accumulates one section per run and the rendered output shows the older
# (pre-fix) runs' failures alongside the current one.
: > "$OUT/hook-stub-e2e.log"
REPO="$T/repo"
mkdir -p "$REPO/hooks" "$REPO/bin" "$REPO/internal" "$T/sqlc" "$T/clean-internal/db"
# $T/sqlc is the repo's ../sqlc (passes the hook's sibling check).
cp scripts/hooks/pre-commit "$REPO/hooks/"
# A clean copy of internal/db for the stub. Pre-create the db/ level so the
# copy preserves it: `cp -r` into a non-existent destination copies the
# SOURCE's contents, which would drop the db/ level.
cp -r internal/db "$T/clean-internal/"
test -f "$T/clean-internal/db/query.sql.go"
# Stub: `make generate` succeeds and writes the CLEAN generated tree into the
# repo's working dir — simulates the real fork regenerating the correct files.
# It cd's into the repo: the plan's cwd is the project checkout, and an
# un-cd'd stub would destroy the project's own internal/.
R="$REPO"
printf '#!/bin/sh\ncd %s && rm -rf internal && mkdir -p internal/db && cp -r %s/clean-internal/db/. internal/db\n' "$R" "$T" > "$REPO/bin/make"
chmod +x "$REPO/bin/make"
git -C "$REPO" init -q
git -C "$REPO" -c user.name=t -c user.email=t@t commit -q --allow-empty -m base
git -C "$REPO" config user.name t
git -C "$REPO" config user.email t@t
cp -r internal/db "$REPO/internal/"
test -f "$REPO/internal/db/query.sql.go"
git -C "$REPO" add hooks internal
git -C "$REPO" commit -qm clean

# Corrupt a generated file and stage it: the corruption is in the INDEX.
echo '// corrupted by a hand-typed sqlc generate' >> "$REPO/internal/db/query.sql.go" || { echo "FAIL: corrupt step errored"; exit 1; }
git -C "$REPO" add internal || { echo "FAIL: stage step errored"; exit 1; }

# Run the hook: it must exit 1 (block) with the make-generate message.
out=$(cd "$REPO" && PATH="$REPO/bin:$PATH" ./hooks/pre-commit 2>&1); rc=$?
echo "corrupt-case rc=$rc" >> "$OUT/hook-stub-e2e.log"
echo "$out" >> "$OUT/hook-stub-e2e.log"
if [ "$rc" -ne 1 ]; then
  echo "FAIL: expected rc=1, got rc=$rc" >> "$OUT/hook-stub-e2e.log"
  exit 1
fi
if ! echo "$out" | grep -q "make generate"; then
  echo "FAIL: missing 'make generate' in output" >> "$OUT/hook-stub-e2e.log"
  exit 1
fi
echo "PASS: corrupt staged → hook exited 1 with make-generate message" >> "$OUT/hook-stub-e2e.log"

# Restore the clean content from HEAD to the working tree AND the index,
# then run the hook: staged now matches generated, so it must pass.
git -C "$REPO" checkout HEAD -- internal/db/query.sql.go || { echo "FAIL: restore step errored"; exit 1; }
git -C "$REPO" add internal || { echo "FAIL: re-stage step errored"; exit 1; }
out=$(cd "$REPO" && PATH="$REPO/bin:$PATH" ./hooks/pre-commit 2>&1); rc=$?
echo "clean-case rc=$rc" >> "$OUT/hook-stub-e2e.log"
if [ "$rc" -ne 0 ]; then
  echo "FAIL: expected rc=0, got rc=$rc" >> "$OUT/hook-stub-e2e.log"
  echo "$out" >> "$OUT/hook-stub-e2e.log"
  exit 1
fi
echo "PASS: clean staged → hook exited 0" >> "$OUT/hook-stub-e2e.log"
echo OK
```

## Block 3 — missing ../sqlc sibling: hook skips with a warning, not a block

The hook must not block commits on a machine without the sibling checkout;
CI is the gate there.

```sh
produces=hook-skip.log inputs=scripts/hooks/pre-commit
set -e
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/hooks"
cp scripts/hooks/pre-commit "$T/hooks/"
git -C "$T" init -q
# Run from a repo whose parent has no ../sqlc: the hook must skip, not block.
(cd "$T" && ./hooks/pre-commit > "$OUT/hook-skip.log" 2>&1); rc=$?
test "$rc" -eq 0
grep -q 'skipping internal/db check' "$OUT/hook-skip.log"
grep -q 'authoritative gate' "$OUT/hook-skip.log"
echo OK
```

## Block 4 — gen-stage check: the stage's own done-condition, and that it discriminates

The spec for this node is "when the project declares a gen stage, its generated
code is current on the base tree: the stage would change nothing."

Part 1 proves the check ITSELF discriminates, in a scratch repo so it can run
anywhere: a fake gen stage (a `make` stub that rewrites `internal/db`) is run
against a committed tree that the stage WOULD change — the check must fail —
and against a tree the stage would leave untouched — the check must pass.
This is the exact failure mode the hook and CI gate catch: a hand-typed
`sqlc generate` on PATH leaves a tree the real stage would change.

Part 2 runs the REAL stage (`make generate` — the Makefile builds the `../sqlc`
fork and refuses a PATH fallback) on this checkout and requires
`git status --porcelain -- internal/db` to be empty. When the `../sqlc`
sibling is absent from the machine (as in this sandbox — recorded in
gen-real.log), the real stage cannot run and the check records that and
passes, deferring to the authoritative gate: the CI subtask's step runs the
same real stage where the sibling is cloned in.

```sh produces=gen-real.log inputs=scripts/hooks/pre-commit,Makefile
# --- Part 1: the check discriminates ----------------------------------------
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
S="$T/repo"
mkdir -p "$S/bin" "$S/internal/db"
git -C "$S" init -q
git -C "$S" config user.name t
git -C "$S" config user.email t@t
# The fake gen stage: writes a file with a CURRENT marker.
printf '#!/bin/sh\ncd %s && printf "generated by stage, current: %%s\\n" STAGE > internal/db/gen.go\n' "$S" > "$S/bin/make"
chmod +x "$S/bin/make"
# Committed tree is STALE: it predates the stage's output.
printf 'generated by stage, current: OUTDATED\n' > "$S/internal/db/gen.go"
git -C "$S" add internal
git -C "$S" commit -qm base
# Run the stage, then the check.
(cd "$S" && PATH="$S/bin:$PATH" make generate)
git -C "$S" status --porcelain -- internal/db > "$T/stale.status"
[ -s "$T/stale.status" ]
echo "PASS: check flags a tree the stage would change: $(head -1 "$T/stale.status")"
# Now commit what the stage produced: the tree is CURRENT.
git -C "$S" add internal
git -C "$S" commit -qm current
(cd "$S" && PATH="$S/bin:$PATH" make generate)
git -C "$S" status --porcelain -- internal/db > "$T/current.status"
[ ! -s "$T/current.status" ]
echo "PASS: check passes on a tree the stage changes nothing"
# --- Part 2: the real stage on this checkout --------------------------------
SIB="$(git rev-parse --show-toplevel)/../sqlc"
PLUGIN="$(git rev-parse --show-toplevel)/../sqlc-go-codegen-metaquery"
{
  echo "toplevel=$(git rev-parse --show-toplevel)"
  echo "sibling sqlc present: $([ -d "$SIB" ] && echo yes || echo no)"
  echo "sibling plugin present: $([ -d "$PLUGIN" ] && echo yes || echo no)"
} > "$OUT/gen-real.log"
if [ -d "$SIB" ]; then
  make generate > "$T/gen.log" 2>&1
  rc=$?
  cat "$T/gen.log" >> "$OUT/gen-real.log"
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: 'make generate' exited $rc" >> "$OUT/gen-real.log"
    exit 1
  fi
  git status --porcelain -- internal/db > "$T/db.status"
  if [ -s "$T/db.status" ]; then
    {
      echo "FAIL: 'make generate' changed internal/db — the committed generated code is not current:"
      cat "$T/db.status"
    } >> "$OUT/gen-real.log"
    exit 1
  fi
  echo "PASS: make generate ran and left internal/db unchanged" >> "$OUT/gen-real.log"
else
  echo "SKIP (authoritative gate is CI): the ../sqlc sibling is absent from this machine,"
  echo "so the real gen stage cannot run here. The CI step clones the fork and plugin"
  echo "as siblings and runs the same 'make generate' + empty-diff check."
fi >> "$OUT/gen-real.log"
```
