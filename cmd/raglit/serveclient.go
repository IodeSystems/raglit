package main

// MCP client mode: when `serve` is configured with a daemon URL, its tools proxy
// to the daemon's HTTP surface (the gat httpd from P2) instead of opening the
// index locally. This completes the client/daemon split (item 1): many `serve`
// instances share ONE daemon's storage, LLM, and queue. The tool CONTRACTS are
// identical to embedded mode (same addRaglitTools definitions); only the backing
// changes. Daemon JSON responses are passed through, minus huma's `$schema`.

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
)

// daemonToolHandlers builds the MCP tool handlers as thin proxies to the daemon
// at base.
func daemonToolHandlers(base string, defLimit int) toolHandlers {
	return toolHandlers{
		search: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError("query is required"), nil
			}
			v := url.Values{"q": {q}}
			setIf(v, "index", req.GetString("index", ""))
			if n := req.GetInt("limit", defLimit); n > 0 {
				v.Set("n", strconv.Itoa(n))
			}
			return proxyGet(base, "/search", v)
		},

		ingest: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u, err := req.RequireString("url")
			if err != nil {
				return mcp.NewToolResultError("url is required"), nil
			}
			b, err := daemonPostJSON(base, "/ingest", map[string]any{
				"targets": []string{u}, "index": req.GetString("index", ""), "title": req.GetString("title", ""),
			})
			if err != nil {
				return mcp.NewToolResultErrorFromErr("ingest", err), nil
			}
			// Reshape the daemon's {queued,job_ids,index} into the MCP tool's
			// {job_id,index,state,url} contract.
			var r struct {
				JobIDs []int64 `json:"job_ids"`
				Index  string  `json:"index"`
			}
			_ = json.Unmarshal(b, &r)
			var jobID int64
			if len(r.JobIDs) > 0 {
				jobID = r.JobIDs[0]
			}
			out, _ := json.Marshal(map[string]any{"job_id": jobID, "index": r.Index, "state": "pending", "url": u})
			return mcp.NewToolResultText(string(out)), nil
		},

		status: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v := url.Values{}
			setIf(v, "index", req.GetString("index", ""))
			return proxyGet(base, "/status", v)
		},

		listIndexes: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return proxyGet(base, "/indexes", nil)
		},

		listDocuments: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v := url.Values{}
			setIf(v, "name", req.GetString("name", ""))
			setIf(v, "index", req.GetString("index", ""))
			return proxyGet(base, "/api/find-documents", v)
		},

		getDocument: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path, err := req.RequireString("path")
			if err != nil {
				return mcp.NewToolResultError("path is required"), nil
			}
			v := url.Values{"path": {path}}
			setIntIf(v, "page", req.GetInt("page", 0))
			setIntIf(v, "from", req.GetInt("from", 0))
			setIntIf(v, "to", req.GetInt("to", 0))
			setIntIf(v, "max_chars", req.GetInt("max_chars", 0))
			setIf(v, "index", req.GetString("index", ""))
			return proxyGet(base, "/api/get-document", v)
		},

		ocr: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			b, err := daemonPostJSON(base, "/api/ocr", map[string]any{
				"path": req.GetString("path", ""), "data": req.GetString("data", ""), "mime": req.GetString("mime", ""),
			})
			if err != nil {
				return mcp.NewToolResultErrorFromErr("ocr", err), nil
			}
			return mcp.NewToolResultText(string(stripSchema(b))), nil
		},
	}
}

// proxyGet performs a daemon GET and returns its (schema-stripped) JSON as the
// tool result, surfacing a daemon error as a tool error.
func proxyGet(base, path string, v url.Values) (*mcp.CallToolResult, error) {
	b, err := daemonGet(base, path, v)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("daemon", err), nil
	}
	return mcp.NewToolResultText(string(stripSchema(b))), nil
}

func setIf(v url.Values, key, val string) {
	if val != "" {
		v.Set(key, val)
	}
}

func setIntIf(v url.Values, key string, n int) {
	if n != 0 {
		v.Set(key, strconv.Itoa(n))
	}
}

// stripSchema removes huma's injected "$schema" field so the proxied JSON matches
// the embedded-mode tool output exactly.
func stripSchema(b []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	if _, ok := m["$schema"]; !ok {
		return b
	}
	delete(m, "$schema")
	if out, err := json.Marshal(m); err == nil {
		return out
	}
	return b
}
