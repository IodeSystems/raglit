# Test plan: st_1m7jd8mda7qebaycz7bref

## F27 identity job cancel — measured pass

Run both tests that exercise `Store.CancelIdentityJob` and the `/api/identity-jobs/cancel` endpoint.
- Store: pending row deleted, running refused, terminal refused, re-enqueue after cancel.
- HTTP: POST removes a pending row, second cancel returns 400.

```sh produces=store_test.log,http_test.log inputs=identityqueue.go,identityqueue_test.go,cmd/raglit/httpd.go,cmd/raglit/httpd_test.go
set -e
set -o pipefail
go test -v -run 'TestCancelIdentityJob$' . 2>&1 | tee $OUT/store_test.log
go test -v -run 'TestGatDaemon_CancelIdentityJobPOST' ./cmd/raglit 2>&1 | tee $OUT/http_test.log
```
