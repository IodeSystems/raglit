# Test plan: st_1m7j8hmkp2gt1bk46f2zbn

The four new F50 test functions pass: branch list with parents, branch diff with
added/changed/deleted markers, empty branch list, the branch tools open no write
path, and the read-surface regression guard.

```sh inputs=cmd/raglit/mcphttp_test.go
go test -count=1 -run 'TestMCP_(BranchToolsLocalNamesAndFields|BranchListEmpty|BranchToolsChangeNothing|ReadSurfaceUntouched)' \
  github.com/iodesystems/raglit/cmd/raglit 2>&1
```
