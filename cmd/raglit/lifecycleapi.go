package main

// Index lifecycle for a client with only a URL and a key: make an index, keep
// it fresh from a directory, put documents in it. The CLI's versions of these
// write to a project config on the client's disk, which such a client has none
// of, so these are daemon state.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/iodesystems/gwag/v2/gw/gat"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/iodesystems/raglit"
)

func registerLifecycleOps(api huma.API, g *gat.Gateway, reg *raglit.Registry, watch *watcher) {
	op := func(id, method, path, summary string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary}
	}
	gat.Register(api, g, op("createIndex", http.MethodPost, "/api/index/create",
		"Make an index the caller owns, with its hint and caption profile."), createIndexOp(reg))
	if watch == nil {
		return
	}
	gat.Register(api, g, op("listDirWatches", http.MethodGet, "/api/index/watches",
		"Directories watched into the indexes the caller can read."), listDirWatchesOp(reg, watch))
	gat.Register(api, g, op("addDirWatch", http.MethodPost, "/api/index/watches",
		"Watch a daemon-host directory into an index the caller owns."), addDirWatchOp(reg, watch))
	gat.Register(api, g, op("removeDirWatch", http.MethodDelete, "/api/index/watches",
		"Stop watching a directory. What was indexed stays."), removeDirWatchOp(reg, watch))
}

type createIndexIn struct {
	Index string `query:"index"`
	Body  struct {
		Hint           string `json:"hint,omitempty"`
		CaptionProfile string `json:"caption_profile,omitempty" doc:"legal, docs, or empty for the corpus's shape"`
	}
}

func createIndexOp(reg *raglit.Registry) func(context.Context, *createIndexIn) (*okOut, error) {
	return func(ctx context.Context, in *createIndexIn) (*okOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixNano()
		if in.Body.Hint != "" {
			if err := st.SetIndexHint(in.Body.Hint, now); err != nil {
				return nil, huma.Error500InternalServerError("hint", err)
			}
		}
		if in.Body.CaptionProfile != "" {
			if err := st.SetCaptionProfile(in.Body.CaptionProfile, now); err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

type dirWatchesOut struct {
	Body struct {
		Watches []dirWatch `json:"watches"`
	}
}

func listDirWatchesOp(reg *raglit.Registry, w *watcher) func(context.Context, *indexOnlyIn) (*dirWatchesOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*dirWatchesOut, error) {
		out := &dirWatchesOut{}
		out.Body.Watches = []dirWatch{}
		for _, d := range w.Dirs() {
			if (in.Index == "" || in.Index == d.Index) && reg.CanRead(ctx, d.Index) {
				out.Body.Watches = append(out.Body.Watches, d)
			}
		}
		return out, nil
	}
}

type addDirWatchIn struct {
	Index string `query:"index"`
	Body  struct {
		Dir string `json:"dir" required:"true" doc:"absolute directory on the daemon's host"`
	}
}

func addDirWatchOp(reg *raglit.Registry, w *watcher) func(context.Context, *addDirWatchIn) (*okOut, error) {
	return func(ctx context.Context, in *addDirWatchIn) (*okOut, error) {
		if err := requireHostPaths(ctx, reg, []string{in.Body.Dir}); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(in.Body.Dir) {
			return nil, huma.Error400BadRequest("dir must be absolute")
		}
		if _, err := writeIdx(ctx, reg, in.Index); err != nil {
			return nil, err
		}
		if err := w.AddDir(raglit.NormalizeIndexName(in.Index), filepath.Clean(in.Body.Dir)); err != nil {
			return nil, huma.Error500InternalServerError("watch", err)
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

type removeDirWatchIn struct {
	Index string `query:"index"`
	Dir   string `query:"dir"`
}

func removeDirWatchOp(reg *raglit.Registry, w *watcher) func(context.Context, *removeDirWatchIn) (*okOut, error) {
	return func(ctx context.Context, in *removeDirWatchIn) (*okOut, error) {
		if _, err := writeIdx(ctx, reg, in.Index); err != nil {
			return nil, err
		}
		if err := w.RemoveDir(raglit.NormalizeIndexName(in.Index), filepath.Clean(in.Dir)); err != nil {
			return nil, huma.Error404NotFound(err.Error())
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

func uploadTool() mcp.Tool {
	return mcp.NewTool("upload",
		mcp.WithDescription(
			"Put a document in an index by sending it, for a daemon that cannot read your files. "+
				"Uploading the same name again replaces the document and re-ingests it; forget "+
				"removes it, by the path this returns. list_documents reports each document's "+
				"content_hash (sha256 of its bytes), so you can send only what changed. Ingest is "+
				"queued. Returns {index, path, job_id}."),
		mcp.WithString("name", mcp.Required(), mcp.Description("relative, slash-separated name: the document's identity in the index")),
		mcp.WithString("text", mcp.Description("document content as text; give text or data")),
		mcp.WithString("data", mcp.Description("document content, base64")),
		mcp.WithString("title", mcp.Description("optional document title")),
		mcp.WithString("index", mcp.Description("target index (default \"default\", created if new)")),
	)
}

func uploadHandler(base string, cv clientView) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError("name is required"), nil
		}
		idx, err := cv.write(req.GetString("index", ""))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("upload", err), nil
		}
		b, err := daemonPostJSON(base, "/api/upload?"+urlValues("index", idx).Encode(), map[string]any{
			"name": name, "text": req.GetString("text", ""), "data": req.GetString("data", ""),
			"title": req.GetString("title", ""),
		})
		if err != nil {
			return mcp.NewToolResultErrorFromErr("upload", err), nil
		}
		return mcp.NewToolResultText(string(stripNSJSON(stripSchema(b), cv.ns))), nil
	}
}

const manageProjectHelp = "Manage this project's indexes. action: create | drop | watch | unwatch | watches. " +
	"`create` makes an index, with an optional hint (what the models should know about this corpus) and " +
	"caption_profile (legal or docs; omit to go by the corpus's shape). An index also appears on first " +
	"upload or ingest. `drop` deletes an index; pass confirm=true (cannot be undone; a shared cache of " +
	"processed documents is kept). `watch` keeps an index fresh from `dir`, an absolute directory on the " +
	"daemon's host that this key was granted: new and changed files are ingested, removed files leave the " +
	"index. `unwatch` stops; what was indexed stays. `watches` lists them."

func manageProjectTool() mcp.Tool {
	return mcp.NewTool("manage_project",
		mcp.WithDescription(manageProjectHelp),
		mcp.WithString("action", mcp.Required(), mcp.Enum("create", "drop", "watch", "unwatch", "watches")),
		mcp.WithString("index", mcp.Description("index name (default \"default\")")),
		mcp.WithString("dir", mcp.Description("watch and unwatch: absolute directory on the daemon's host")),
		mcp.WithString("hint", mcp.Description("create: what the models should know about this corpus")),
		mcp.WithString("caption_profile", mcp.Enum("legal", "docs"), mcp.Description("create: caption rules for prose")),
		mcp.WithBoolean("confirm", mcp.Description("drop: true deletes")),
	)
}

func manageProjectHandler(base string, cv clientView) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action, err := req.RequireString("action")
		if err != nil {
			return mcp.NewToolResultError("action is required"), nil
		}
		idx, err := cv.write(req.GetString("index", ""))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("manage_project", err), nil
		}
		dir := req.GetString("dir", "")
		q := urlValues("index", idx)
		var b []byte
		switch action {
		case "create":
			b, err = daemonPostJSON(base, "/api/index/create?"+q.Encode(), map[string]any{
				"hint": req.GetString("hint", ""), "caption_profile": req.GetString("caption_profile", ""),
			})
		case "drop":
			if !req.GetBool("confirm", false) {
				return mcp.NewToolResultError("drop deletes the index and cannot be undone; pass confirm=true"), nil
			}
			b, err = daemonDelete(base, "/indexes", url.Values{"name": {idx}})
		case "watch":
			b, err = daemonPostJSON(base, "/api/index/watches?"+q.Encode(), map[string]any{"dir": dir})
		case "unwatch":
			q.Set("dir", dir)
			b, err = daemonDelete(base, "/api/index/watches", q)
		case "watches":
			b, err = daemonGet(base, "/api/index/watches", nil)
			if err == nil {
				return mcp.NewToolResultText(string(watchesFor(b, cv))), nil
			}
		default:
			return mcp.NewToolResultError("unknown action " + action), nil
		}
		if err != nil {
			return mcp.NewToolResultErrorFromErr("manage_project "+action, err), nil
		}
		return mcp.NewToolResultText(string(stripSchema(b))), nil
	}
}

// watchesFor is the watch list as this client names its indexes.
func watchesFor(b []byte, cv clientView) []byte {
	var doc struct {
		Watches []dirWatch `json:"watches"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return b
	}
	kept := []dirWatch{}
	for _, d := range doc.Watches {
		if cv.root && !rootIndex(d.Index) {
			continue
		}
		if !cv.root && nsStrip(cv.ns, d.Index) == d.Index {
			continue
		}
		d.Index = nsStrip(cv.ns, d.Index)
		kept = append(kept, d)
	}
	out, err := json.Marshal(map[string]any{"watches": kept})
	if err != nil {
		return b
	}
	return out
}
