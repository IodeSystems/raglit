package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const version = "0.1.0"

// runServe exposes the home's indexes as a stdio MCP server. It hosts a SET of
// named indexes (Slice G): search defaults to ALL of them (RRF-merged, each hit
// tagged with its index), ingest targets one, and index_status/list_indexes
// report across them. The search result shape stays what ragnotify.ParseHits
// consumes, so one server still drives both the explicit tool and the proactive
// (live-watch) channel — an agent scopes the watch by passing `index`.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	_, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	defLimit := fs.Int("n", 8, "default max results")
	embed := fs.Bool("embed", false, "embed ingested fragments (enables vector search)")
	fs.Parse(args)

	reg, err := raglit.OpenRegistry(homeOf())
	if err != nil {
		return err
	}
	defer reg.Close()
	lf.resolve(homeOf())
	if *embed {
		if err := lf.requireEmbed(); err != nil {
			return err
		}
		reg.SetEmbedder(lf.embedder())
	}

	// One background loop drains every index's queue round-robin (per-index
	// workers cached). A configured model gives PDF OCR + LLM text segmentation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runIndexWorkers(ctx, reg, lf, homeOf())

	s := server.NewMCPServer("raglit", version)
	s.AddTool(
		mcp.NewTool("search",
			mcp.WithDescription(
				"Search the document index(es). Returns ranked fragments as JSON "+
					"{hits:[{index,doc_id,title,page,score,snippet}]}, best first. `index` "+
					"selects one index or a comma-separated set; omit it to search ALL "+
					"(results merged with reciprocal-rank fusion, each hit tagged by index)."),
			mcp.WithString("query", mcp.Required(), mcp.Description("natural-language or keyword query")),
			mcp.WithString("index", mcp.Description("index name, or comma-separated names; empty = all")),
			mcp.WithNumber("limit", mcp.Description("max results (default 8)")),
		),
		searchHandler(reg, *defLimit),
	)
	s.AddTool(
		mcp.NewTool("ingest",
			mcp.WithDescription(
				"Queue a URL for LAZY ingestion into an index (returns a job id; a background "+
					"worker fetches + segments + indexes it). file://<path> or http(s)://... ; "+
					"PDFs are OCR'd when a vision model is configured. `index` names the target "+
					"index (default \"default\", created if new). Poll index_status."),
			mcp.WithString("url", mcp.Required(), mcp.Description("file://<path> or http(s)://<url>")),
			mcp.WithString("index", mcp.Description("target index (default \"default\")")),
			mcp.WithString("title", mcp.Description("optional document title")),
		),
		ingestHandler(reg),
	)
	s.AddTool(
		mcp.NewTool("index_status",
			mcp.WithDescription(
				"Report index + ingest-queue status as JSON: documents/fragments, "+
					"done/running/pending/failed job counts, a recent rate (jobs/min), and "+
					"pending items each with an ETA. `index` selects one; omit to aggregate all."),
			mcp.WithString("index", mcp.Description("index name; empty = aggregate all")),
		),
		statusHandler(reg),
	)
	s.AddTool(
		mcp.NewTool("list_indexes",
			mcp.WithDescription("List the available indexes with their document/fragment counts (JSON)."),
		),
		listHandler(reg),
	)
	// OCR tool: image/PDF bytes → paged text, via the cheap→gibberish→VLM cascade.
	// Offered only when something can OCR (a vision model and/or a cheap engine is
	// configured); otherwise it would fail every call, so it is omitted.
	if ocr := buildToolOCR(lf, homeOf()); ocr != nil {
		s.AddTool(
			mcp.NewTool("ocr",
				mcp.WithDescription(
					"OCR a document to paged text. Give `path` (file://… or a local path) OR "+
						"base64 `data` (+ optional `mime`). A PDF is rasterized to its embedded "+
						"page images; anything else is treated as one image. Returns JSON "+
						"{pages:[{page,text,engine}], engines:{<engine>:count}} where engine is the "+
						"cheap engine's name when its result passed the gibberish gate, else "+
						"\"vision\". Owns rasterization + engine routing — the caller just sends bytes."),
				mcp.WithString("path", mcp.Description("file://<path> or a local path to a PDF/image")),
				mcp.WithString("data", mcp.Description("base64-encoded document bytes (alternative to path)")),
				mcp.WithString("mime", mcp.Description("content type hint, e.g. application/pdf or image/png")),
			),
			ocrHandler(ocr),
		)
	}
	return server.ServeStdio(s)
}

// runIndexWorkers drains every index's queue, round-robin, caching one worker
// per index. New indexes (created via ingest) are picked up on the next round.
func runIndexWorkers(ctx context.Context, reg *raglit.Registry, lf *llmFlags, home raglit.Home) {
	workers := map[string]*raglit.Worker{}
	for ctx.Err() == nil {
		did := false
		for _, name := range reg.Names() {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			w := workers[name]
			if w == nil {
				w = buildWorker(st, lf, home)
				workers[name] = w
			}
			if processed, _ := w.ProcessOne(ctx); processed {
				did = true
			}
		}
		if !did {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

// selectIndexes resolves the `index` argument to a concrete set of names: empty
// → all; else the comma-separated list (unknown names simply return nothing).
func selectIndexes(reg *raglit.Registry, arg string) []string {
	arg = strings.TrimSpace(arg)
	if arg == "" || arg == "all" {
		return reg.Names()
	}
	var out []string
	for _, n := range strings.Split(arg, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func searchHandler(reg *raglit.Registry, defLimit int) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query is required"), nil
		}
		limit := req.GetInt("limit", defLimit)
		names := selectIndexes(reg, req.GetString("index", ""))

		// Search each selected index; over-fetch, then RRF-merge across indexes.
		lists := map[string][]raglit.Hit{}
		for _, name := range names {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			hits, err := st.Search(q, limit*2)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("search", err), nil
			}
			lists[name] = hits
		}
		merged := rrfMerge(lists, limit)

		b, err := json.Marshal(taggedHits(merged))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode", err), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

func ingestHandler(reg *raglit.Registry) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url, err := req.RequireString("url")
		if err != nil {
			return mcp.NewToolResultError("url is required"), nil
		}
		name := req.GetString("index", "default")
		st, err := reg.Get(name)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("open index", err), nil
		}
		id, err := st.Enqueue(url, req.GetString("title", ""))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("enqueue", err), nil
		}
		b, _ := json.Marshal(map[string]any{"job_id": id, "index": name, "state": "pending", "url": url})
		return mcp.NewToolResultText(string(b)), nil
	}
}

func statusHandler(reg *raglit.Registry) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		names := selectIndexes(reg, req.GetString("index", ""))
		var agg raglit.Status
		for _, name := range names {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			s, err := st.IndexStatus()
			if err != nil {
				return mcp.NewToolResultErrorFromErr("status", err), nil
			}
			agg.Documents += s.Documents
			agg.Fragments += s.Fragments
			agg.Done += s.Done
			agg.Running += s.Running
			agg.Pending += s.Pending
			agg.Failed += s.Failed
			if s.RatePerMin > agg.RatePerMin {
				agg.RatePerMin = s.RatePerMin
			}
			agg.Items = append(agg.Items, s.Items...)
		}
		b, err := json.Marshal(agg)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode", err), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

func listHandler(reg *raglit.Registry) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		type idx struct {
			Name      string `json:"name"`
			Documents int    `json:"documents"`
			Fragments int    `json:"fragments"`
		}
		out := struct {
			Indexes []idx `json:"indexes"`
		}{Indexes: []idx{}}
		for _, name := range reg.Names() {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			s, _ := st.IndexStatus()
			out.Indexes = append(out.Indexes, idx{name, s.Documents, s.Fragments})
		}
		b, _ := json.Marshal(out)
		return mcp.NewToolResultText(string(b)), nil
	}
}

// indexedHit pairs a hit with the index it came from.
type indexedHit struct {
	index string
	hit   raglit.Hit
}

// rrfMerge fuses the per-index ranked lists into one, reciprocal-rank fusion
// (scale-free, so cross-index scores needn't be comparable), best first.
func rrfMerge(lists map[string][]raglit.Hit, limit int) []indexedHit {
	const k = 60.0
	type acc struct {
		ih    indexedHit
		score float64
	}
	m := map[string]*acc{}
	for idx, hits := range lists {
		for rank, h := range hits {
			key := fmt.Sprintf("%s\x00%d", idx, h.ID)
			a := m[key]
			if a == nil {
				a = &acc{ih: indexedHit{idx, h}}
				m[key] = a
			}
			a.score += 1.0 / (k + float64(rank))
		}
	}
	out := make([]*acc, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > limit {
		out = out[:limit]
	}
	res := make([]indexedHit, len(out))
	for i, a := range out {
		res[i] = a.ih
	}
	return res
}

// taggedHits renders merged hits in the ragnotify.ParseHits shape, plus an
// `index` tag per hit.
func taggedHits(hits []indexedHit) any {
	type outHit struct {
		Index   string  `json:"index"`
		DocID   string  `json:"doc_id"`
		Title   string  `json:"title"`
		Page    int     `json:"page"`
		Score   float64 `json:"score"`
		Snippet string  `json:"snippet"`
	}
	out := struct {
		Hits []outHit `json:"hits"`
	}{Hits: []outHit{}}
	for _, ih := range hits {
		h := ih.hit
		title := h.Title
		if title == "" {
			title = h.Path
		}
		out.Hits = append(out.Hits, outHit{
			Index: ih.index, DocID: h.Path, Title: title, Page: h.Page,
			Score: h.Score, Snippet: clip(oneLine(h.Text), 300),
		})
	}
	return out
}
