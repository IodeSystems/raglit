package main

import (
	"net/http"
	"strings"
	"testing"
)

// Pause over HTTP shows in the index list, and resume clears it.
func TestPauseIndexOp(t *testing.T) {
	srv, _ := settingsServer(t, `{}`)
	if code, body := doAs(t, "POST", srv.URL+"/api/index/pause?index=default", "", `{"paused":true}`); code != http.StatusOK {
		t.Fatalf("pause: %d %s", code, body)
	}
	if _, body := doAs(t, "GET", srv.URL+"/indexes", "", ""); !strings.Contains(body, `"paused":true`) {
		t.Fatalf("index list does not show the pause: %s", body)
	}
	if code, body := doAs(t, "POST", srv.URL+"/api/index/pause?index=default", "", `{"paused":false}`); code != http.StatusOK {
		t.Fatalf("resume: %d %s", code, body)
	}
	if _, body := doAs(t, "GET", srv.URL+"/indexes", "", ""); strings.Contains(body, `"paused"`) {
		t.Fatalf("index list still shows a pause: %s", body)
	}
}

// The ignore list round-trips over HTTP, and POST /ingest skips what it refuses
// instead of failing the batch.
func TestIgnoreOpsAndIngestSkips(t *testing.T) {
	srv, _ := settingsServer(t, `{}`)
	code, body := doAs(t, "PUT", srv.URL+"/api/index/ignore?index=default", "", `{"patterns":["**/dist/**"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"**/dist/**"`) || !strings.Contains(body, "node_modules") {
		t.Fatalf("put: %d %s", code, body)
	}
	if _, body := doAs(t, "GET", srv.URL+"/api/index/ignore?index=default", "", ""); !strings.Contains(body, `"**/dist/**"`) {
		t.Fatalf("get: %s", body)
	}
	dir := t.TempDir()
	code, body = doAs(t, "POST", srv.URL+"/ingest", "", `{"index":"default","targets":["`+dir+`/a/dist/x.md","`+dir+`/node_modules/y.md","`+dir+`/ok.md"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"queued":1`) || !strings.Contains(body, `"ignored":2`) {
		t.Fatalf("ingest: %d %s", code, body)
	}
}
