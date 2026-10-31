package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Batch MCP ingest and forget (dun 2026-10-23): dun indexes 100+ files (skills,
// rules, MCP docs) as one batch, and one call per file was slow. `ingest`
// takes paths[] beside url, and `forget` takes paths[] for one index.

// batchTargets expands the paths of one call the way `raglit ingest` expands
// its arguments: a URL passes, a directory is walked, a file becomes absolute.
// A path that does not exist is reported, not fatal to the rest.
func batchTargets(paths []string) (targets, missing []string) {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		t, err := expandIngestTargets([]string{p})
		if err != nil {
			missing = append(missing, p)
			continue
		}
		targets = append(targets, t...)
	}
	return targets, missing
}

// forgetPaths are the document paths one forget names. A document keeps the
// spelling it was ingested by: a file:// URL stays a URL, a bare path becomes
// absolute. So "file:///a.md" and "/a.md" each drop both spellings.
func forgetPaths(p string) []string {
	p = strings.TrimSpace(p)
	bare := strings.TrimPrefix(p, "file://")
	if strings.Contains(bare, "://") {
		return []string{p} // another scheme: only as given
	}
	abs, err := filepath.Abs(bare)
	if err != nil {
		abs = bare
	}
	return []string{abs, "file://" + abs}
}

// batchResult is the reply to an ingest that named paths.
type batchResult struct {
	Index   string   `json:"index"`
	Queued  int      `json:"queued"`
	JobIDs  []int64  `json:"job_ids"`
	Ignored int      `json:"ignored,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

func batchText(r batchResult) *mcp.CallToolResult {
	b, _ := json.Marshal(r)
	return mcp.NewToolResultText(string(b))
}

func forgetTool() mcp.Tool {
	return mcp.NewTool("forget",
		mcp.WithDescription(
			"Drop documents from an index, with their page images. Files on the user's disk are untouched; an "+
				"uploaded file is deleted with its document. Paths are file paths "+
				"or file:// URLs; either spelling drops the document however it was ingested. Nothing stops a later "+
				"ingest from adding a path back. Returns {index, forgotten, paths}."),
		mcp.WithString("index", mcp.Description("index (default: the default index)")),
		mcp.WithArray("paths", mcp.Required(), mcp.WithStringItems(), mcp.Description("documents to drop")),
	)
}

// forgetHandler drops each path through forget(index, path) and reports what
// went. resolve maps the tool's index argument to the store's name.
func forgetHandler(resolve func(string) (string, error), display func(string) string,
	forget func(index, path string) error) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		paths := req.GetStringSlice("paths", nil)
		if len(paths) == 0 {
			return mcp.NewToolResultError("paths is required"), nil
		}
		idx, err := resolve(req.GetString("index", ""))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("forget", err), nil
		}
		var done []string
		for _, p := range paths {
			if strings.TrimSpace(p) == "" {
				continue
			}
			fps := forgetPaths(p)
			for _, fp := range fps {
				if err := forget(idx, fp); err != nil {
					return mcp.NewToolResultErrorFromErr("forget "+fp, err), nil
				}
			}
			done = append(done, fps[0])
		}
		b, _ := json.Marshal(map[string]any{"index": display(idx), "forgotten": len(done), "paths": done})
		return mcp.NewToolResultText(string(b)), nil
	}
}
