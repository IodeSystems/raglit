# Test plan: st_1m876dpxefe26nq32kfkm6

Check the environment for bench/retrieval prerequisites: LLM URL, snapshot directory, Go toolchain, and build the raglit binary.

```sh produces=env-check.txt inputs=go.mod
set -e
# Check environment for bench/retrieval prerequisites
RAGLIT_LLM_URL_VAL="${RAGLIT_LLM_URL:-NOT_SET}"
echo "RAGLIT_LLM_URL: $RAGLIT_LLM_URL_VAL"

# Check for snapshot directories
SNAP_FOUND=0
for d in ~/local/src/iodesystems/raglit.bench ~/local/src/iodesystems/raglit.snap /tmp/raglit-snap; do
  if [ -d "$d" ]; then
    echo "SNAPSHOT FOUND: $d"
    ls "$d" 2>/dev/null | head -5
    SNAP_FOUND=1
  fi
done
[ "$SNAP_FOUND" = "0" ] && echo "NO SNAPSHOT DIRECTORY FOUND"

# Check Go and build
go version
echo "Building raglit..."
go build -o /tmp/raglit-bench . 2>&1
echo "BUILT OK: $(/tmp/raglit-bench --version 2>&1 | head -1)"

# Write summary
{
  echo "llm_url=$RAGLIT_LLM_URL_VAL"
  echo "snapshot_found=$SNAP_FOUND"
  echo "go=$(go version)"
  echo "raglit_binary=/tmp/raglit-bench"
} > "$OUT/env-check.txt"
cat "$OUT/env-check.txt"
```
