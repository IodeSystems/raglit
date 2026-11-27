# Test plan: st_1m87cmbfxdnqq5xnrj2hax

Run TestClaimAndPageReadingWhileWritersCommit in isolation with -count=1000. If the fix at store.go:353 (_txlock=immediate&_pragma=busy_timeout(30000)) is holding, all 1000 iterations pass with no SQLITE_BUSY_SNAPSHOT failure.

```sh produces=count1000.log
set -o pipefail
go test -count=1000 -run TestClaimAndPageReadingWhileWritersCommit ./... 2>&1 | tee $OUT/count1000.log
```
