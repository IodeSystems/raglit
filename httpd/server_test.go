package httpd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandler_MultiProtocolSurface proves the gat gateway stands up and serves
// REST + OpenAPI + GraphQL off one router — the P2 stack, running.
func TestHandler_MultiProtocolSurface(t *testing.T) {
	h, err := Handler("raglit", "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	// REST: the huma operation.
	body := get(t, srv.URL+"/api/health")
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("GET /api/health = %q, want status ok", body)
	}

	// OpenAPI: served by humachi, includes the registered operation.
	spec := get(t, srv.URL+"/openapi.json")
	if !strings.Contains(spec, "/api/health") {
		t.Fatalf("/openapi.json missing the health path")
	}

	// GraphQL: gat mounts POST /graphql (an empty POST is a 4xx, not a 404).
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("/graphql not mounted (404) — gat.RegisterHuma didn't wire it")
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s → HTTP %d", url, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
