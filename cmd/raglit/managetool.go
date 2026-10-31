package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"

	"github.com/iodesystems/raglit"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// The manage_index MCP tool: the index Controls page for an agent. Pause and
// resume, the ignore and allow lists, and purge by pattern. One handler over
// two backings, the local registry (embedded serve) or the daemon's HTTP
// surface (client mode), like every other tool.

// controlState is what manage_index reports after every action.
type controlState struct {
	Index   string   `json:"index"`
	Paused  bool     `json:"paused"`
	Ignore  []string `json:"ignore"`
	Allow   []string `json:"allow"`
	Builtin []string `json:"builtin"`
}

type indexControls interface {
	state(idx string) (controlState, error)
	setPaused(idx string, paused bool) error
	// setLists replaces the lists given; nil leaves one unchanged.
	setLists(idx string, ignore, allow *[]string) error
	purge(idx, pattern string, dry bool) (raglit.PurgeResult, error)
	prune(idx string, dry bool) (raglit.PruneResult, error)
}

const manageIndexHelp = "Operate an index: pause/resume its work, edit the patterns refused at enqueue, " +
	"or purge indexed documents by pattern. action: status | pause | resume | ignore | unignore | " +
	"allow | disallow | purge | prune. A paused index runs no imports, captions or embeds; jobs still queue. " +
	"`ignore PATTERN` refuses matching paths at enqueue on this index. Every index also refuses the " +
	"builtin list (node_modules, mobile build output); `allow PATTERN` exempts matching paths from the " +
	"builtin list on this index. Patterns are globs matched against the absolute path: **/dir/** is a " +
	"directory anywhere, a glob with no / matches the file name. `purge PATTERN` reports what would be " +
	"removed; pass confirm=true to remove it (cannot be undone; files on the user's disk stay, uploaded files " +
	"go). `prune` (no pattern) reports the documents whose local file is gone — a deleted directory or " +
	"worktree — grouped by deleted directory: {documents, dirs:[{dir, documents}], removed, orphan_pages, " +
	"dangling_originals}. They are already out of search; confirm=true removes them and the orphan page " +
	"images. Dangling originals are removed only by the CLI's --delete-orphan-originals. list_indexes reports `prunable` per index. Every " +
	"other action returns the index's resulting state."

func manageIndexTool() mcp.Tool {
	return mcp.NewTool("manage_index",
		mcp.WithDescription(manageIndexHelp),
		mcp.WithString("action", mcp.Required(),
			mcp.Enum("status", "pause", "resume", "ignore", "unignore", "allow", "disallow", "purge", "prune")),
		mcp.WithString("index", mcp.Description("index name (default \"default\")")),
		mcp.WithString("pattern", mcp.Description("glob for ignore, unignore, allow, disallow and purge")),
		mcp.WithBoolean("confirm", mcp.Description("purge and prune: true removes; false or omitted reports what would go")),
	)
}

func manageIndexHandler(c indexControls, resolve func(string) (string, error), display func(string) string) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action, err := req.RequireString("action")
		if err != nil {
			return mcp.NewToolResultError("action is required"), nil
		}
		idx, err := resolve(req.GetString("index", ""))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("manage_index", err), nil
		}
		pattern := req.GetString("pattern", "")
		needPattern := action != "status" && action != "pause" && action != "resume" && action != "prune"
		if needPattern && pattern == "" {
			return mcp.NewToolResultError(action + " needs a pattern"), nil
		}
		reply := func(v any) (*mcp.CallToolResult, error) {
			b, _ := json.Marshal(v)
			return mcp.NewToolResultText(string(b)), nil
		}
		fail := func(err error) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultErrorFromErr("manage_index "+action, err), nil
		}

		if action == "purge" {
			res, err := c.purge(idx, pattern, !req.GetBool("confirm", false))
			if err != nil {
				return fail(err)
			}
			return reply(map[string]any{"index": display(idx), "removed": req.GetBool("confirm", false), "purge": res})
		}
		if action == "prune" {
			res, err := c.prune(idx, !req.GetBool("confirm", false))
			if err != nil {
				return fail(err)
			}
			return reply(map[string]any{"index": display(idx), "prune": res})
		}
		cur, err := c.state(idx)
		if err != nil {
			return fail(err)
		}
		edit := func(list []string, add bool) *[]string {
			out := slices.DeleteFunc(slices.Clone(list), func(p string) bool { return p == pattern })
			if add {
				out = append(out, pattern)
			}
			return &out
		}
		switch action {
		case "status":
		case "pause", "resume":
			err = c.setPaused(idx, action == "pause")
		case "ignore", "unignore":
			err = c.setLists(idx, edit(cur.Ignore, action == "ignore"), nil)
		case "allow", "disallow":
			err = c.setLists(idx, nil, edit(cur.Allow, action == "allow"))
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q", action)), nil
		}
		if err != nil {
			return fail(err)
		}
		if cur, err = c.state(idx); err != nil {
			return fail(err)
		}
		cur.Index = display(idx)
		return reply(cur)
	}
}

// daemonControls backs manage_index with the daemon's HTTP surface.
type daemonControls struct{ base string }

func (c daemonControls) state(idx string) (controlState, error) {
	q := url.Values{"index": {idx}}
	b, err := daemonGet(c.base, "/api/index/ignore", q)
	if err != nil {
		return controlState{}, err
	}
	var lists struct {
		Patterns []string `json:"patterns"`
		Builtin  []string `json:"builtin"`
		Allow    []string `json:"allow"`
	}
	if err := json.Unmarshal(b, &lists); err != nil {
		return controlState{}, err
	}
	b, err = daemonGet(c.base, "/indexes", nil)
	if err != nil {
		return controlState{}, err
	}
	var all struct {
		Indexes []idxRow `json:"indexes"`
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return controlState{}, err
	}
	out := controlState{Index: idx, Ignore: lists.Patterns, Allow: lists.Allow, Builtin: lists.Builtin}
	for _, r := range all.Indexes {
		if r.Name == idx {
			out.Paused = r.Paused
		}
	}
	return out, nil
}

func (c daemonControls) setPaused(idx string, paused bool) error {
	_, err := daemonPostJSON(c.base, "/api/index/pause?"+urlValues("index", idx).Encode(), map[string]any{"paused": paused})
	return err
}

func (c daemonControls) setLists(idx string, ignore, allow *[]string) error {
	body := map[string]any{}
	if ignore != nil {
		body["patterns"] = *ignore
	}
	if allow != nil {
		body["allow"] = *allow
	}
	_, err := daemonDo(c.base, "PUT", "/api/index/ignore?"+urlValues("index", idx).Encode(), body)
	return err
}

func (c daemonControls) purge(idx, pattern string, dry bool) (raglit.PurgeResult, error) {
	var res raglit.PurgeResult
	b, err := daemonPostJSON(c.base, "/api/index/purge?"+urlValues("index", idx).Encode(),
		map[string]any{"pattern": pattern, "dry_run": dry})
	if err != nil {
		return res, err
	}
	return res, json.Unmarshal(b, &res)
}

func (c daemonControls) prune(idx string, dry bool) (raglit.PruneResult, error) {
	var res raglit.PruneResult
	b, err := daemonPostJSON(c.base, "/api/index/prune?"+urlValues("index", idx).Encode(),
		map[string]any{"dry_run": dry})
	if err != nil {
		return res, err
	}
	return res, json.Unmarshal(b, &res)
}
