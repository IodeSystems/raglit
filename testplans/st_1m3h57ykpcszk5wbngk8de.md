# Test plan: st_1m3h57ykpcszk5wbngk8de

The declared check refuses this node with `go: updates to go.mod needed; to update it: go mod tidy`. This branch is 0 commits and `diff` shows no changes against main, so the check is running against the base tree itself — the failure is pre-existing on base, not introduced by this work. This block reproduces it on the clean tree and records the environment state (sibling module dir, which the go.mod relative replace requires, and the go toolchain), so the reviewer has a measured repro rather than a claim.

```sh produces=go-check-repro.log inputs=go.mod,go.sum
{
  echo '=== git status --porcelain (empty = clean tree, nothing from this node) ==='
  git status --porcelain
  echo '=== sibling module dir (target of the relative replace in go.mod) ==='
  ls -d ../sqlc-go-codegen-metaquery 2>&1 | head -1
  echo '=== go version ==='
  go version
  echo '=== GOWORK=off go build ./... (the declared check path) ==='
  GOWORK=off go build ./... 2>&1 | head -30
  echo '=== end of capture (exit codes suppressed: block documents, does not assert pass) ==='
} > "$OUT/go-check-repro.log" 2>&1
cat "$OUT/go-check-repro.log"
```

Diagnoses WHY the base tree fails `go mod tidy`: records the work/toolchain env, then runs `go mod tidy` on copies, diffs go.mod/go.sum against the committed originals, and restores them — so the checkout is left untouched and the log names exactly what the base tree is missing.

```sh produces=go-tidy-diag.log inputs=go.mod,go.sum
{
  echo '=== go.work files in play (GOWORK unset) ==='
  go env GOWORK GOTOOLCHAIN
  ls -la go.work 2>&1 | head -1
  echo '=== GOFLAGS / GONOSUMCHECK-ish env ==='
  go env GOFLAGS GONOSUMDB GONOSUMCHECK GOPROXY 2>&1
  echo '=== go mod tidy -diff would be nice (go1.23+) — using -x off, dry via copy ==='
  cp go.mod /tmp/go.mod.orig
  cp go.sum /tmp/go.sum.orig
  GOWORK=off go mod tidy 2>&1 | tail -20
  echo '=== go.mod diff after tidy ==='
  diff -u /tmp/go.mod.orig go.mod | head -40
  echo '=== go.sum diff stat after tidy ==='
  diff /tmp/go.sum.orig go.sum | head -20
  echo '=== restore originals ==='
  cp /tmp/go.mod.orig go.mod
  cp /tmp/go.sum.orig go.sum
  git status --porcelain
} > "$OUT/go-tidy-diag.log" 2>&1
cat "$OUT/go-tidy-diag.log"
```

Names the drift source: the `replace` in raglit go.mod points at the sibling checkout, so whatever the sibling's go.mod requires is what tidy must satisfy. This block captures the sibling's go.mod verbatim and its git state (clean/dirty, recent commits), so the pin 'base go.mod/go.sum is stale relative to the sibling module's current requirements' has a measured source.

```sh produces=sibling-gomod.log inputs=go.mod
{
  echo '=== sibling ../sqlc-go-codegen-metaquery/go.mod (the replace target) ==='
  cat ../sqlc-go-codegen-metaquery/go.mod
  echo '=== its committed state (is the sibling dirty? that would make it a checkout-local issue) ==='
  git -C ../sqlc-go-codegen-metaquery status --porcelain 2>&1 | head -5
  git -C ../sqlc-go-codegen-metaquery log --oneline -3 2>&1
} > "$OUT/sibling-gomod.log" 2>&1
cat "$OUT/sibling-gomod.log"
```
