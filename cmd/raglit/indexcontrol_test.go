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
