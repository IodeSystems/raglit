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

// Purge over HTTP: a dry run reports, a real one removes.
func TestPurgeOp(t *testing.T) {
	srv, _ := settingsServer(t, `{}`)
	dir := t.TempDir()
	if code, body := doAs(t, "POST", srv.URL+"/ingest", "", `{"index":"default","targets":["`+dir+`/vendor-x/a.md"]}`); code != http.StatusOK {
		t.Fatalf("ingest: %d %s", code, body)
	}
	code, body := doAs(t, "POST", srv.URL+"/api/index/purge?index=default", "", `{"pattern":"**/vendor-x/**","dry_run":true}`)
	if code != http.StatusOK || !strings.Contains(body, `"ingest_jobs":1`) {
		t.Fatalf("dry run: %d %s", code, body)
	}
	if code, body := doAs(t, "POST", srv.URL+"/api/index/purge?index=default", "", `{"pattern":"**/vendor-x/**"}`); code != http.StatusOK {
		t.Fatalf("purge: %d %s", code, body)
	}
	if _, body := doAs(t, "POST", srv.URL+"/api/index/purge?index=default", "", `{"pattern":"**/vendor-x/**","dry_run":true}`); !strings.Contains(body, `"ingest_jobs":0`) {
		t.Fatalf("job left after purge: %s", body)
	}
	if code, _ := doAs(t, "POST", srv.URL+"/api/index/purge?index=default", "", `{"pattern":"  "}`); code != http.StatusUnprocessableEntity {
		t.Errorf("empty pattern: %d, want 422", code)
	}
}

// PUT changes only the list it is given.
func TestIgnorePutKeepsTheOtherList(t *testing.T) {
	srv, _ := settingsServer(t, `{}`)
	doAs(t, "PUT", srv.URL+"/api/index/ignore?index=default", "", `{"patterns":["**/dist/**"]}`)
	_, body := doAs(t, "PUT", srv.URL+"/api/index/ignore?index=default", "", `{"allow":["**/Pods/**"]}`)
	if !strings.Contains(body, `"patterns":["**/dist/**"]`) || !strings.Contains(body, `"allow":["**/Pods/**"]`) {
		t.Fatalf("lists after two PUTs: %s", body)
	}
}

// manage_index over the daemon proxy: pause, ignore, allow and a purge dry run
// and confirm, each answered with the resulting state.
func TestManageIndexTool(t *testing.T) {
	srv, _ := gatTestServer(t) // seeds file:///notes.md
	h := daemonToolHandlers(srv.URL, 8, "", nil).manageIndex
	if out := callTool(t, h, map[string]any{"action": "pause", "index": "default"}); !strings.Contains(out, `"paused":true`) {
		t.Fatalf("pause: %s", out)
	}
	if out := callTool(t, h, map[string]any{"action": "resume", "index": "default"}); !strings.Contains(out, `"paused":false`) {
		t.Fatalf("resume: %s", out)
	}
	if out := callTool(t, h, map[string]any{"action": "ignore", "pattern": "**/dist/**"}); !strings.Contains(out, `"ignore":["**/dist/**"]`) {
		t.Fatalf("ignore: %s", out)
	}
	if out := callTool(t, h, map[string]any{"action": "allow", "pattern": "**/Pods/**"}); !strings.Contains(out, `"allow":["**/Pods/**"]`) ||
		!strings.Contains(out, `"ignore":["**/dist/**"]`) {
		t.Fatalf("allow: %s", out)
	}
	if out := callTool(t, h, map[string]any{"action": "unignore", "pattern": "**/dist/**"}); !strings.Contains(out, `"ignore":[]`) {
		t.Fatalf("unignore: %s", out)
	}
	if out := callTool(t, h, map[string]any{"action": "purge", "pattern": "notes.md"}); !strings.Contains(out, `"documents":1`) || !strings.Contains(out, `"removed":false`) {
		t.Fatalf("purge dry run: %s", out)
	}
	callTool(t, h, map[string]any{"action": "purge", "pattern": "notes.md", "confirm": true})
	if out := callTool(t, h, map[string]any{"action": "purge", "pattern": "notes.md"}); !strings.Contains(out, `"documents":0`) {
		t.Fatalf("document left after purge: %s", out)
	}
}
