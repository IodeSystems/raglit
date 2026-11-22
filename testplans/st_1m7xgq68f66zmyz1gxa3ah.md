# Test plan: st_1m7xgq68f66zmyz1gxa3ah

Block 1 — the machine. Record PATH, node/npm presence, and the Go toolchain, to $OUT/env.log. This dev machine HAS node/npm; the node-free guarantee is proven in block 2 by a trap that fails the build if node/npm are ever invoked.

```sh produces=env.log
set -e
{
echo "=== PATH (as given) ==="
echo "$PATH"
echo
echo "=== node/npm on PATH as given ==="
for t in node npm; do
  if command -v "$t" >/dev/null 2>&1; then echo "$t: PRESENT at $(command -v $t)"; else echo "$t: NOT on PATH"; fi
done
echo
echo "=== go (as given) ==="
command -v go && go version
} > "$OUT/env.log" 2>&1
cat "$OUT/env.log"
```

Block 2 — `go install` with no node. Arm a trap on node/npm/npx (a PATH shadow that writes a marker and fails if called), then run `go install ./cmd/raglit` in an isolated GOCACHE/GOBIN/GOPATH. A build that completes with the marker absent proves node/npm were never invoked — the "machine without node" half of the promise, independent of where the real node lives. Records the stamped module version and the web/dist files the tree carried.

```sh produces=install.log,raglit inputs=cmd/raglit
set -e
WORK=$(mktemp -d)
# The Go module cache is read-only (go makes it so), so plain rm -rf fails on it
# and would set a non-zero exit code on the way out. Clean up tolerantly.
trap 'chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK" 2>/dev/null || true' EXIT

# Trap node/npm: if the build EVER invokes them, the marker is written and the
# call fails — a build that completes proves node/npm were never called,
# regardless of where the real ones live on this box.
TRAPDIR="$WORK/trap"
mkdir -p "$TRAPDIR"
for t in node npm npx; do
  printf '#!/bin/sh\necho "FORBIDDEN: %s invoked during go install" >> "%s/node-invoked"\nexit 97\n' "$t" "$WORK" > "$TRAPDIR/$t"
  chmod +x "$TRAPDIR/$t"
done

GODIR=$(dirname "$(command -v go)")
CLEANPATH="$TRAPDIR:$GODIR:/usr/bin:/bin:/usr/sbin:/sbin"

# Isolated module cache / GOBIN so this never touches the real $HOME.
export GOCACHE="$WORK/gocache"
export GOBIN="$WORK/bin"
export GOPATH="$WORK/gopath"
export PATH="$CLEANPATH"

{
  echo "=== go install under a PATH that traps node/npm ==="
  echo "node resolves to: $(command -v node)"
  echo "npm  resolves to: $(command -v npm)"
  echo
  echo "=== go install ./cmd/raglit (module github.com/iodesystems/raglit) ==="
} > "$OUT/install.log"
if go install ./cmd/raglit >> "$OUT/install.log" 2>&1; then
  INST=0
else
  INST=1
fi
{
  echo "install exit: $INST"
  echo
  if [ -f "$WORK/node-invoked" ]; then
    echo "!!! node/npm WERE invoked during the build:"
    cat "$WORK/node-invoked"
  else
    echo "node/npm were NEVER invoked during the build (trap was armed, marker absent)."
  fi
  echo
  echo "=== installed binary ==="
  ls -l "$GOBIN/raglit"
  echo
  echo "=== module version stamped in the binary (go version -m, key lines) ==="
  go version -m "$GOBIN/raglit" > "$WORK/stamp.txt" 2>&1
  grep -E 'github.com/iodesystems/raglit$|vcs\.(time|revision|modified)' "$WORK/stamp.txt" || true
  echo
  echo "=== web/dist embedded files (from the tree this installed) ==="
  ls -l web/dist web/dist/assets
} >> "$OUT/install.log" 2>&1
[ "$INST" -eq 0 ] || { cat "$OUT/install.log"; exit 1; }
cp "$GOBIN/raglit" "$OUT/raglit"
cat "$OUT/install.log"
```

Block 3 — daemon up, UI loads. Start the INSTALLED binary (from block 2) as a daemon with a fresh empty RAGLIT_ROOT/RAGLIT_HOME and a fresh port. Poll /api/health until it answers `"status":"ok"`, then GET / (expect 200, text/html, the placeholder document with id="root" + /assets/app.js) and GET that asset (expect it served as javascript, not the fallback page). The page loading — not a blank one and not the "UI not built" fallback — is the promise.

```sh produces=ui.log inputs=cmd/raglit,web/dist/index.html,web/dist/assets/app.js
set -e
ROOT=$(mktemp -d)
HOMEDIR=$(mktemp -d)
PORT=$(awk 'BEGIN{srand(); print 42000+int(rand()*10000)}')
ADDR="127.0.0.1:$PORT"
export RAGLIT_NO_AUTOBUILD=1
export RAGLIT_ROOT="$ROOT"
export RAGLIT_HOME="$HOMEDIR"

"$OUT/raglit" daemon --root "$ROOT" --addr "$ADDR" > "$ROOT/daemon.log" 2>&1 &
DPID=$!
trap 'kill $DPID 2>/dev/null; wait $DPID 2>/dev/null; rm -rf "$ROOT" "$HOMEDIR"' EXIT

# Poll /api/health until it answers ok (max ~30s).
HEALTH=""
for i in $(seq 1 60); do
  if curl -sf "http://$ADDR/api/health" > "$ROOT/health.json" 2>/dev/null; then
    HEALTH=$(cat "$ROOT/health.json")
    case "$HEALTH" in *'"status":"ok"'*) break;; esac
  fi
  sleep 0.5
done
echo "=== /api/health ==="
echo "$HEALTH"
case "$HEALTH" in *'"status":"ok"'*) : ;; *)
  echo "DAEMON DID NOT BECOME HEALTHY" >&2
  echo "---- daemon.log ----" >&2
  cat "$ROOT/daemon.log" >&2
  exit 1
  ;;
esac
echo

# The daemon has auth on (health reports "auth":true and names its admin token
# file). Every route except /api/health needs a bearer token — the documented
# client path is to read the token file health names. Extract it.
TOKEN_FILE=$(tr -d '[:space:]' <<<"$HEALTH" | grep -oE '"admin_token":"[^"]+"' | cut -d'"' -f4)
echo "admin token file (named by /api/health): $TOKEN_FILE"
TOKEN=""
[ -n "$TOKEN_FILE" ] && [ -f "$TOKEN_FILE" ] && TOKEN=$(cat "$TOKEN_FILE")
AUTH=()
[ -n "$TOKEN" ] && AUTH=(-H "Authorization: Bearer $TOKEN")

# GET / — the UI document.
curl -s "${AUTH[@]}" -D "$ROOT/root.headers" "http://$ADDR/" -o "$ROOT/root.html"
echo "=== GET / ==="
grep -i '^HTTP/\|^Content-Type:' "$ROOT/root.headers"
echo "---- body ----"
cat "$ROOT/root.html"
echo
echo

# Extract the /assets/*.js the document names and fetch it.
SRC=$(grep -oE '/assets/[^"]+\.js' "$ROOT/root.html" | head -1)
echo "asset named by document: $SRC"
[ -n "$SRC" ] || { echo "document names no /assets/*.js" >&2; exit 1; }
curl -s "${AUTH[@]}" -D "$ROOT/asset.headers" "http://$ADDR$SRC" -o "$ROOT/asset.js"
echo "=== GET $SRC ==="
grep -i '^HTTP/\|^Content-Type:' "$ROOT/asset.headers"
echo "---- body (first 5 lines) ----"
head -5 "$ROOT/asset.js"

# Assertions, written to the artifact.
{
  echo "=== health ==="
  echo "$HEALTH"
  echo
  echo "=== GET / headers ==="
  grep -i '^HTTP/\|^Content-Type:' "$ROOT/root.headers"
  echo "=== GET / body ==="
  cat "$ROOT/root.html"
  echo
  echo "=== asset: $SRC headers ==="
  grep -i '^HTTP/\|^Content-Type:' "$ROOT/asset.headers"
  echo "=== asset body ==="
  cat "$ROOT/asset.js"
  echo
  echo "=== assertions ==="
  echo "root has id=\"root\": $(grep -q 'id="root"' "$ROOT/root.html" && echo YES || echo NO)"
  echo "root content-type html: $(grep -qi '^Content-Type: text/html' "$ROOT/root.headers" && echo YES || echo NO)"
  echo "asset content-type javascript: $(grep -qi '^Content-Type: .*javascript' "$ROOT/asset.headers" && echo YES || echo NO)"
  echo "asset is not the fallback page: $(grep -q 'id=\"root\"' "$ROOT/asset.js" && echo NO || echo YES)"
} > "$OUT/ui.log" 2>&1

# Fail the block if the document is the "UI not built" fallback page.
if grep -qi 'The review UI is not in this binary' "$ROOT/root.html"; then
  echo "!!! served the 'UI not built' fallback page, not the placeholder" >&2
  cat "$ROOT/root.html" >&2
  exit 1
fi
grep -q 'id="root"' "$ROOT/root.html" || { echo "document has no id=root" >&2; exit 1; }
grep -qi '^Content-Type: .*javascript' "$ROOT/asset.headers" || { echo "asset not served as javascript" >&2; exit 1; }
echo "=== ui.log ==="
cat "$OUT/ui.log"
```

Block 4 — tagged-release risk check (static). Confirm the web/dist placeholder is tracked in git, the release workflow's build job runs the UI build (`npm ci`/`npm run build`) BEFORE the per-target `go build` so tarballs carry the real bundle, and that the release smoke step runs only `--help` + `doctor --json` — it does NOT start the daemon or load the UI. That last gap is recorded, not fixed (out of scope).

```sh produces=release-check.log inputs=web/.gitignore,web/dist/index.html,web/dist/assets/app.js,.github/workflows/release.yml
set -e
{
  echo "=== web/dist placeholder files tracked in git ==="
  git ls-files web/dist
  echo
  echo "=== .gitignore un-ignores the two placeholder files ==="
  grep -nE 'dist/(index\.html|assets)' web/.gitignore
  echo
  echo "=== release workflow: UI build BEFORE go build (build job order) ==="
  # The Web UI step (npm ci / npm run build) must precede the per-target go build.
  awk 'NR>=1{print NR": "$0}' .github/workflows/release.yml \
    | grep -nE 'npm ci|npm run build|go build -trimpath|name: Web UI|name: Build and package' | head
  echo
  echo "=== release smoke step (does it touch the UI?) ==="
  sed -n '/- name: Smoke/,/uses: actions\/upload-artifact/p' .github/workflows/release.yml
  echo
  echo "=== does the smoke step start the daemon or GET the UI? ==="
  if sed -n '/- name: Smoke/,/upload-artifact/p' .github/workflows/release.yml | grep -qE 'daemon|/api/health|curl|GET /|/assets/'; then
    echo "YES — smoke exercises the daemon/UI"
  else
    echo "NO — smoke runs --help and doctor --json only; it does NOT start the daemon or load the UI (a gap worth noting, not fixing here)"
  fi
  echo
  echo "=== conclusion ==="
  echo "go install path ships the committed placeholder by design (web/embed.go); tagged tarballs carry the real bundle because npm run build runs before go build. The release smoke step does not exercise the UI."
} > "$OUT/release-check.log" 2>&1
cat "$OUT/release-check.log"
```
