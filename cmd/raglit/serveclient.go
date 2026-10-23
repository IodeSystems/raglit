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
// at base. ns is the project namespace: index names in requests are prefixed
// ("<ns>__<local>", "<ns>__*" for all) and stripped back out of responses, so on
// the shared daemon this project only sees its own indexes.
func daemonToolHandlers(base string, defLimit int, ns string, shared []string) toolHandlers {
	return daemonToolHandlersFor(base, defLimit, clientView{ns: ns, shared: shared})
}

// daemonToolHandlersFor is daemonToolHandlers for a client view: with cwd set,
// a call that names no index reads the indexes scoped to cwd (index scopes).
func daemonToolHandlersFor(base string, defLimit int, cv clientView) toolHandlers {
	ns, shared := cv.ns, cv.shared
	return toolHandlers{
		forget: forgetHandler(func(name string) (string, error) {
			return cv.write(name)
		}, func(name string) string { return nsStrip(ns, name) }, func(index, path string) error {
			return daemonForgetIn(base, index, path)
		}),
		manageIndex: manageIndexHandler(daemonControls{base}, func(name string) (string, error) {
			return cv.write(name)
		}, func(name string) string { return nsStrip(ns, name) }),
		search: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError("query is required"), nil
			}
			v := url.Values{"q": {q}}
			v.Set("index", cv.read(req.GetString("index", "")))
			setIf(v, "path", req.GetString("path", ""))
			if n := req.GetInt("limit", defLimit); n > 0 {
				v.Set("n", strconv.Itoa(n))
			}
			if !req.GetBool("siblings", true) {
				v.Set("siblings", "false")
			}
			return proxyGet(base, "/search", v, ns)
		},

		searchFigures: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError("query is required"), nil
			}
			v := url.Values{"q": {q}}
			v.Set("index", cv.read(req.GetString("index", "")))
			setIf(v, "path", req.GetString("path", ""))
			if n := req.GetInt("limit", defLimit); n > 0 {
				v.Set("n", strconv.Itoa(n))
			}
			return proxyGet(base, "/search-figures", v, ns)
		},

		ingest: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u := req.GetString("url", "")
			paths := req.GetStringSlice("paths", nil)
			if u == "" && len(paths) == 0 {
				return mcp.NewToolResultError("url or paths is required"), nil
			}
			idx, err := cv.write(req.GetString("index", ""))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("ingest", err), nil
			}
			if len(paths) > 0 {
				targets, missing := batchTargets(append(paths, u))
				r := batchResult{Index: nsStrip(ns, idx), JobIDs: []int64{}, Missing: missing}
				if len(targets) > 0 {
					b, err := daemonPostJSON(base, "/ingest", map[string]any{
						"targets": targets, "index": idx, "title": req.GetString("title", ""), "scope": cv.claim,
					})
					if err != nil {
						return mcp.NewToolResultErrorFromErr("ingest", err), nil
					}
					var out struct {
						Queued  int     `json:"queued"`
						JobIDs  []int64 `json:"job_ids"`
						Ignored int     `json:"ignored"`
					}
					_ = json.Unmarshal(b, &out)
					r.Queued, r.Ignored = out.Queued, out.Ignored
					if out.JobIDs != nil {
						r.JobIDs = out.JobIDs
					}
				}
				return batchText(r), nil
			}
			b, err := daemonPostJSON(base, "/ingest", map[string]any{
				"targets": []string{u}, "index": idx, "title": req.GetString("title", ""), "scope": cv.claim,
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
			out, _ := json.Marshal(map[string]any{"job_id": jobID, "index": nsStrip(ns, r.Index), "state": "pending", "url": u})
			return mcp.NewToolResultText(string(out)), nil
		},

		status: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v := url.Values{"index": {cv.read(req.GetString("index", ""))}}
			return proxyGet(base, "/status", v, ns)
		},

		listIndexes: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			b, err := daemonGet(base, "/indexes", nil)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("daemon", err), nil
			}
			if cv.cwd != "" {
				// Scope mode: every index, with its scopes, so a caller can name
				// one it is not in the scope of.
				return mcp.NewToolResultText(string(stripSchema(b))), nil
			}
			return mcp.NewToolResultText(string(filterIndexList(stripSchema(b), ns, shared))), nil
		},

		getFields: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path, err := req.RequireString("path")
			if err != nil {
				return mcp.NewToolResultError("path is required"), nil
			}
			v := url.Values{"path": {path}}
			v.Set("index", cv.read(req.GetString("index", "")))
			return proxyGet(base, "/api/fields", v, ns)
		},

		listDocuments: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v := url.Values{}
			setIf(v, "name", req.GetString("name", ""))
			v.Set("index", cv.read(req.GetString("index", "")))
			return proxyGet(base, "/api/find-documents", v, ns)
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
			v.Set("index", cv.read(req.GetString("index", "")))
			return proxyGet(base, "/api/get-document", v, ns)
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

// proxyGet performs a daemon GET and returns its JSON as the tool result — with
// huma's $schema stripped and every "index" field un-namespaced (ns prefix
// removed) — surfacing a daemon error as a tool error.
func proxyGet(base, path string, v url.Values, ns string) (*mcp.CallToolResult, error) {
	b, err := daemonGet(base, path, v)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("daemon", err), nil
	}
	return mcp.NewToolResultText(string(stripNSJSON(stripSchema(b), ns))), nil
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
