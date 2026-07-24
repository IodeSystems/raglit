package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func callTool(t *testing.T, h server.ToolHandlerFunc, args map[string]any) string {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("not text content: %T", res.Content[0])
	}
	if res.IsError {
		t.Fatalf("tool error: %s", tc.Text)
	}
	return tc.Text
}

// TestServeClient_ProxiesToDaemon drives the MCP client-mode tool handlers
// against a real gat daemon (httptest) and checks they return the daemon's data,
// $schema-stripped, in the tool contract shapes.
func TestServeClient_ProxiesToDaemon(t *testing.T) {
	srv, _ := gatTestServer(t)              // seeds file:///notes.md ("…refresh token rotates…") + a pending job
	h := daemonToolHandlers(srv.URL, 8, "") // ns="" → no namespacing (proxy contract unchanged)

	if out := callTool(t, h.listIndexes, nil); !strings.Contains(out, `"documents":1`) {
		t.Fatalf("list_indexes: %s", out)
	}
	if out := callTool(t, h.status, map[string]any{"index": "default"}); !strings.Contains(out, `"pending":1`) {
		t.Fatalf("index_status: %s", out)
	}
	// search: proxied hit, and huma's $schema must be stripped.
	out := callTool(t, h.search, map[string]any{"query": "refresh token", "index": "default"})
	if !strings.Contains(out, "notes.md") {
		t.Fatalf("search: %s", out)
	}
	if strings.Contains(out, "$schema") {
		t.Fatalf("search: $schema not stripped: %s", out)
	}
	if out := callTool(t, h.listDocuments, map[string]any{"name": "notes"}); !strings.Contains(out, "file:///notes.md") {
		t.Fatalf("list_documents: %s", out)
	}
	if out := callTool(t, h.getDocument, map[string]any{"path": "notes"}); !strings.Contains(out, "rotates on every use") {
		t.Fatalf("get_document: %s", out)
	}
	// ingest: reshaped into the {job_id,…} tool contract.
	if out := callTool(t, h.ingest, map[string]any{"url": "file:///new.md"}); !strings.Contains(out, `"job_id"`) {
		t.Fatalf("ingest: %s", out)
	}
}

func TestStripSchema(t *testing.T) {
	got := string(stripSchema([]byte(`{"$schema":"http://x/y.json","hits":[]}`)))
	if got != `{"hits":[]}` {
		t.Fatalf("stripSchema = %s", got)
	}
	// No $schema → unchanged.
	if got := string(stripSchema([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Fatalf("stripSchema passthrough = %s", got)
	}
}
