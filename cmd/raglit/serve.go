package main

import (
	"context"
	"encoding/json"
	"flag"

	"github.com/iodesystems/raglit"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const version = "0.1.0"

// runServe exposes the index as a stdio MCP server with one `search` tool. Any
// MCP client (Claude Desktop, agentkit's mcpmgr) can spawn it. The result JSON
// is deliberately the shape ragnotify.ParseHits already consumes
// (hits[].doc_id/title/score/snippet), so the SAME server drives BOTH channels:
// the model calls search explicitly, AND agentkit wraps it in a DocFinder for
// proactive pings — no second integration.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	openStore := addStoreFlags(fs)
	defLimit := fs.Int("n", 8, "default max results")
	fs.Parse(args)

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	s := server.NewMCPServer("raglit", version)
	s.AddTool(
		mcp.NewTool("search",
			mcp.WithDescription(
				"Search the local document index (BM25 over document:page:fragment). "+
					"Returns ranked fragments as JSON {hits:[{doc_id,title,page,score,snippet}]}, "+
					"best first. doc_id is the source path — cite it or fetch the file for full text."),
			mcp.WithString("query", mcp.Required(), mcp.Description("natural-language or keyword query")),
			mcp.WithNumber("limit", mcp.Description("max results (default 8)")),
		),
		searchHandler(store, *defLimit),
	)
	return server.ServeStdio(s)
}

func searchHandler(store *raglit.Store, defLimit int) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query is required"), nil
		}
		hits, err := store.Search(q, req.GetInt("limit", defLimit))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("search failed", err), nil
		}
		b, err := hitsJSON(hits)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode", err), nil
		}
		return mcp.NewToolResultText(b), nil
	}
}

// hitsJSON renders search hits as the wire shape both consumers read: the model
// reads it directly, and agentkit's ragnotify.ParseHits parses it for the
// proactive channel (hits[].doc_id/title/score/snippet). Kept separate from the
// MCP plumbing so the contract is unit-testable against ParseHits.
func hitsJSON(hits []raglit.Hit) (string, error) {
	type outHit struct {
		DocID   string  `json:"doc_id"`
		Title   string  `json:"title"`
		Page    int     `json:"page"`
		Score   float64 `json:"score"`
		Snippet string  `json:"snippet"`
	}
	out := struct {
		Hits []outHit `json:"hits"`
	}{Hits: []outHit{}}
	for _, h := range hits {
		title := h.Title
		if title == "" {
			title = h.Path
		}
		out.Hits = append(out.Hits, outHit{
			DocID: h.Path, Title: title, Page: h.Page,
			Score: h.Score, Snippet: clip(oneLine(h.Text), 300),
		})
	}
	b, err := json.Marshal(out)
	return string(b), err
}
