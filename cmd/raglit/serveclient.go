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
			setIf(v, "session", req.GetString("session", ""))
			setIf(v, "trigger_step", req.GetString("trigger_step", ""))
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
						"targets": targets, "index": idx, "title": req.GetString("title", ""), "scope": cv.claim, "explain": req.GetBool("explain", false), "analyze": req.GetBool("analyze", false),
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
				"targets": []string{u}, "index": idx, "title": req.GetString("title", ""), "scope": cv.claim, "explain": req.GetBool("explain", false), "analyze": req.GetBool("analyze", false),
			})
			if err != nil {
				return mcp.NewToolResultErrorFromErr("ingest", err), nil
			}
			// Reshape the daemon's {queued,job_ids,index} into the MCP tool's
			// {job_id,index,state,url} contract.
			var r struct {
				JobIDs  []int64         `json:"job_ids"`
				Index   string          `json:"index"`
				Explain json.RawMessage `json:"explain"`
				Analyze json.RawMessage `json:"analyze"`
			}
			_ = json.Unmarshal(b, &r)
			// Explain and analyze return reports, not a job: pass them through
			// verbatim so the caller sees what the daemon computed.
			if len(r.Explain) > 0 || len(r.Analyze) > 0 {
				out := map[string]any{"index": nsStrip(ns, r.Index), "url": u}
				if len(r.Explain) > 0 {
					out["explain"] = r.Explain
				}
				if len(r.Analyze) > 0 {
					out["analyze"] = r.Analyze
				}
				ob, _ := json.Marshal(out)
				return mcp.NewToolResultText(string(ob)), nil
			}
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
			if cv.root {
				return mcp.NewToolResultText(string(filterRootIndexList(stripSchema(b)))), nil
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

		queryFields: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body := struct {
				Constraints []struct {
					Field string `json:"field"`
					Op    string `json:"op"`
					Value string `json:"value"`
				} `json:"constraints"`
				Sort  *string `json:"sort,omitempty"`
				Dir   *string `json:"dir,omitempty"`
				Limit *int    `json:"limit,omitempty"`
			}{}
			if cs := req.GetString("constraints", ""); cs != "" {
				// The tool takes `constraints` as a JSON string so the MCP
				// contract is flat strings; decode it here.
				if err := json.Unmarshal([]byte(cs), &body.Constraints); err != nil {
					return mcp.NewToolResultError("constraints must be a JSON array of {field, op, value}"), nil
				}
			}
			if s := req.GetString("sort", ""); s != "" {
				body.Sort = &s
			}
			if d := req.GetString("dir", ""); d != "" {
				body.Dir = &d
			}
			if n := req.GetInt("limit", 0); n > 0 {
				body.Limit = &n
			}
			v := url.Values{}
			v.Set("index", cv.read(req.GetString("index", "")))
			b, err := daemonDo(base, "POST", "/api/fields/query?"+v.Encode(), body)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("query_fields", err), nil
			}
			return mcp.NewToolResultText(string(stripNSJSON(stripSchema(b), ns))), nil
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
			setIntIf(v, "offset", req.GetInt("offset", 0))
			setIf(v, "query", req.GetString("query", ""))
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

		branchList: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// The daemon lists ALL readable branches; `index` narrows to one
			// parent. A branch the caller cannot read never reaches here: the
			// daemon already filtered it, and the ns strip keeps names local.
			parent := req.GetString("index", "")
			b, err := daemonGet(base, "/api/branches", nil)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("branch_list", err), nil
			}
			var out struct {
				Branches []struct {
					Name           string `json:"name"`
					Parent         string `json:"parent"`
					CreatedAt      int64  `json:"created_at"`
					LastAccessedAt int64  `json:"last_accessed_at"`
					Documents      int    `json:"documents"`
				} `json:"branches"`
			}
			if err := json.Unmarshal(b, &out); err != nil {
				return mcp.NewToolResultText(string(stripSchema(b))), nil
			}
			for i := range out.Branches {
				out.Branches[i].Name = nsStrip(ns, out.Branches[i].Name)
				out.Branches[i].Parent = nsStrip(ns, out.Branches[i].Parent)
				if parent != "" && out.Branches[i].Parent != parent {
					out.Branches[i].Name = ""
				}
			}
			kept := out.Branches[:0]
			for _, br := range out.Branches {
				if br.Name != "" {
					kept = append(kept, br)
				}
			}
			out.Branches = kept
			res, _ := json.Marshal(out)
			return mcp.NewToolResultText(string(res)), nil
		},

		branchDiff: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			branch, err := req.RequireString("branch")
			if err != nil {
				return mcp.NewToolResultError("branch is required"), nil
			}
			v := url.Values{"name": {cv.read(branch)}}
			b, err := daemonGet(base, "/api/branches/diff", v)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("branch_diff", err), nil
			}
			// Unmarshal and nsStrip name+parent: stripNSJSON (proxyGet) rewrites
			// only "index" fields, leaving name/parent namespaced — the two tools
			// would disagree on the name contract.
			var out struct {
				Name    string `json:"name"`
				Parent  string `json:"parent"`
				Changes []struct {
					Path       string `json:"path"`
					Change     string `json:"change"`
					Hash       string `json:"hash,omitempty"`
					ParentHash string `json:"parent_hash,omitempty"`
					Title      string `json:"title,omitempty"`
				} `json:"changes"`
			}
			if err := json.Unmarshal(stripSchema(b), &out); err != nil {
				return mcp.NewToolResultText(string(stripSchema(b))), nil
			}
			out.Name = nsStrip(ns, out.Name)
			out.Parent = nsStrip(ns, out.Parent)
			res, _ := json.Marshal(out)
			return mcp.NewToolResultText(string(res)), nil
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
