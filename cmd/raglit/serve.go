package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
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
	client := addClientFlags(fs) // --daemon + --embedded
	defLimit := fs.Int("n", 8, "default max results")
	embed := fs.Bool("embed", false, "embedded mode: embed ingested fragments")
	fs.Parse(args)

	// DEFAULT: proxy the MCP tools to the shared per-user daemon, auto-starting it
	// if none is running — so N Claude sessions are N thin clients to ONE daemon
	// (single writer + worker pool + LLM caller), not N processes fighting over the
	// same index. --embedded opts out and runs the index in-process (single-session).
	durl, ns, err := client(homeOf, false)
	if err != nil {
		return err
	}
	if durl != "" {
		s := server.NewMCPServer("raglit", version)
		addRaglitTools(s, daemonToolHandlers(durl, *defLimit, ns, projectShared(homeOf)))
		return server.ServeStdio(s)
	}

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
	if ie := buildImageEmbedder(homeOf()); ie != nil {
		reg.SetImageEmbedder(ie)
	}
	reg.SetIdentifier(lf.identifier(homeOf()))

	// One background loop drains every index's queue round-robin (per-index
	// workers cached). A configured model gives PDF OCR + LLM text segmentation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runIndexWorkers(ctx, reg, lf, homeOf(), nil) // embedded serve: single index, no shared pool
	go runIdentityWorkers(ctx, reg, homeOf())

	s := server.NewMCPServer("raglit", version)
	addRaglitTools(s, toolHandlers{
		search:        searchHandler(reg, *defLimit),
		searchFigures: searchFiguresHandler(reg, *defLimit),
		ingest:        ingestHandler(reg),
		status:        statusHandler(reg),
		listIndexes:   listHandler(reg),
		listDocuments: listDocumentsHandler(reg),
		getDocument:   getDocumentHandler(reg),
		ocr:           ocrHandler(buildToolOCR(lf, homeOf())),
	})
	return server.ServeStdio(s)
}

// toolHandlers is raglit's MCP tool surface, supplied either from the local
// registry (embedded mode) or as daemon proxies (client mode — serveclient.go).
type toolHandlers struct {
	search        server.ToolHandlerFunc
	searchFigures server.ToolHandlerFunc
	ingest        server.ToolHandlerFunc
	status        server.ToolHandlerFunc
	listIndexes   server.ToolHandlerFunc
	listDocuments server.ToolHandlerFunc
	getDocument   server.ToolHandlerFunc
	ocr           server.ToolHandlerFunc
}

// addRaglitTools registers the tool definitions once, backed by the given
// handlers. One tool contract, either backing.
func addRaglitTools(s *server.MCPServer, h toolHandlers) {
	s.AddTool(
		mcp.NewTool("search",
			mcp.WithDescription(
				"Search the document index(es). Returns ranked fragments as JSON "+
					"{hits:[{index,doc_id,title,page,score,snippet,origin}]}, best first. A hit "+
					"with origin=\"identity\" is the document's GENERATED caption/summary, not "+
					"its own words — use it to find the document, never to quote it. `index` "+
					"selects one index or a comma-separated set; omit it to search ALL "+
					"(results merged with reciprocal-rank fusion, each hit tagged by index)."),
			mcp.WithString("query", mcp.Required(), mcp.Description("natural-language or keyword query")),
			mcp.WithString("index", mcp.Description("index name, or comma-separated names; empty = all")),
			mcp.WithString("path", mcp.Description("constrain to documents whose path starts with this prefix (a subtree; use a trailing / for a clean directory)")),
			mcp.WithNumber("limit", mcp.Description("max results (default 8)")),
		),
		h.search,
	)
	s.AddTool(
		mcp.NewTool("search_figures",
			mcp.WithDescription(
				"Semantic search over FIGURES (diagrams/charts/tables the VLM described while "+
					"OCR'ing). Returns ranked figures as JSON {figures:[{index,media_id,path,title,"+
					"page,description,image_path,fragment_id,score}]}, best first. Each figure is "+
					"embedded by its description (or its image, when an image embedder is configured). "+
					"Needs an --embed'd index. `index` selects one/comma-separated set; omit = all."),
			mcp.WithString("query", mcp.Required(), mcp.Description("natural-language query about a figure's content")),
			mcp.WithString("index", mcp.Description("index name, or comma-separated names; empty = all")),
			mcp.WithString("path", mcp.Description("constrain to documents whose path starts with this prefix (a subtree)")),
			mcp.WithNumber("limit", mcp.Description("max results (default 8)")),
		),
		h.searchFigures,
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
		h.ingest,
	)
	s.AddTool(
		mcp.NewTool("index_status",
			mcp.WithDescription(
				"Report index + ingest-queue status as JSON: documents/fragments, "+
					"done/running/pending/failed job counts, a recent rate (jobs/min), and "+
					"pending items each with an ETA. `index` selects one; omit to aggregate all."),
			mcp.WithString("index", mcp.Description("index name; empty = aggregate all")),
		),
		h.status,
	)
	s.AddTool(
		mcp.NewTool("list_indexes",
			mcp.WithDescription("List the available indexes with their document/fragment counts (JSON)."),
		),
		h.listIndexes,
	)
	s.AddTool(
		mcp.NewTool("list_documents",
			mcp.WithDescription(
				"List indexed documents (filenames/paths) with their fragment/page counts as JSON "+
					"{documents:[{index,path,title,fragments,pages,vision,frag_mode}]}. `name` filters to documents "+
					"whose path or title contains that substring (case-insensitive). `index` selects one "+
					"index or a comma-separated set; omit to list across ALL. Use this to find a document, "+
					"then get_document to read its text."),
			mcp.WithString("name", mcp.Description("case-insensitive substring filter over path/title; empty = all")),
			mcp.WithString("index", mcp.Description("index name, or comma-separated names; empty = all")),
		),
		h.listDocuments,
	)
	s.AddTool(
		mcp.NewTool("get_document",
			mcp.WithDescription(
				"Get a document's indexed TEXT (reassembled from fragments in page order) as JSON "+
					"{index,path,title,pages:[{page,text}],text,truncated}. `path` is a document path "+
					"(the doc_id from a search hit) OR a unique filename substring — ambiguous matches "+
					"return an error listing the candidates. Optional `page` (single) or `from`/`to` "+
					"(inclusive page range); `max_chars` caps the WHOLE response — `text` and the "+
					"`pages` array are cut at the same offset, so it bounds what you take back. "+
					"`index` restricts the lookup; omit to resolve across all indexes."),
			mcp.WithString("path", mcp.Required(), mcp.Description("document path or a unique filename substring")),
			mcp.WithNumber("page", mcp.Description("single page to return (overrides from/to)")),
			mcp.WithNumber("from", mcp.Description("first page of an inclusive range")),
			mcp.WithNumber("to", mcp.Description("last page of an inclusive range")),
			mcp.WithNumber("max_chars", mcp.Description("cap on the returned text, pages included (0 = uncapped)")),
			mcp.WithString("index", mcp.Description("restrict lookup to this index (or comma-separated set); empty = all")),
		),
		h.getDocument,
	)
	// ocr tool: any document → paged text, via the format router (extract.go).
	// A PDF uses its text layer where present and OCRs the scanned pages; office/
	// markup goes through pandoc; images run the OCR cascade; text is read. Useful
	// even with no vision model (text-layer / pandoc / plain paths).
	s.AddTool(
		mcp.NewTool("ocr",
			mcp.WithDescription(
				"Extract a document to paged text. Give `path` (file://… or a local path) OR "+
					"base64 `data` (+ optional `mime`). Handles PDF (text layer where present, "+
					"OCR the scanned pages), office/markup (docx, odt, epub, html, pptx via "+
					"pandoc), images (OCR), and plain text. Returns JSON "+
					"{pages:[{page,text,engine}], engines:{<engine>:count}} where engine is "+
					"\"text\" (text layer / pandoc / plain), the cheap OCR engine's name, or "+
					"\"vision\". Owns format detection + extraction — the caller just sends bytes."),
			mcp.WithString("path", mcp.Description("file://<path> or a local path to a document")),
			mcp.WithString("data", mcp.Description("base64-encoded document bytes (alternative to path)")),
			mcp.WithString("mime", mcp.Description("content type hint, e.g. application/pdf, image/png, or a docx type")),
		),
		h.ocr,
	)
}

// runIndexWorkers drains every index's queue, round-robin, caching one worker
// per index. New indexes (created via ingest) are picked up on the next round.
// runIdentityWorkers drains every index's captioning queue, ONE INDEX AT A TIME.
//
// Serial across indexes on purpose. The slot budget belongs to the endpoint, not
// to an index: two indexes each draining "two at a time" is four requests at a
// server that runs two, and the extra pair waits inside it where nothing here
// can see or resume them. So one index is drained to empty, then the next.
//
// Separate from the ingest workers because the two are different work with the
// same scarce resource — an ingest job is minutes of OCR over many pages, a
// caption is one bounded call — and putting captions on the ingest queue would
// have made a 400-document sweep block every incoming document behind it.
func runIdentityWorkers(ctx context.Context, reg *raglit.Registry, home raglit.Home) {
	cfg, _, _ := raglit.LoadConfig(home)
	slots := cfg.IdentitySlots
	for ctx.Err() == nil {
		did := false
		for _, name := range reg.Names() {
			if ctx.Err() != nil {
				return
			}
			st, err := reg.Existing(name)
			if err != nil {
				continue
			}
			// A row left 'running' by a dead process is work nobody is doing.
			if n, err := st.ReclaimIdentityJobs(); err == nil && n > 0 {
				log.Printf("raglit: identity queue %s: requeued %d orphaned job(s)", name, n)
			}
			q, err := st.IdentityQueue()
			if err != nil || q.Pending == 0 {
				continue
			}
			log.Printf("raglit: identity queue %s: %d pending (%d at a time)", name, q.Pending, effectiveSlots(slots))
			n, werr := (&raglit.IdentityWorker{Store: st, Slots: slots}).Drain(ctx)
			if werr != nil && !errors.Is(werr, raglit.ErrNoIdentifier) {
				log.Printf("raglit: identity queue %s: %v", name, werr)
			}
			if n > 0 {
				did = true
				log.Printf("raglit: identity queue %s: %d done", name, n)
			}
		}
		if !did {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func effectiveSlots(n int) int {
	if n <= 0 {
		return raglit.DefaultIdentitySlots
	}
	return n
}

func runIndexWorkers(ctx context.Context, reg *raglit.Registry, lf *llmFlags, home raglit.Home, pool *raglit.Pool) {
	// Strand nothing on upgrade. An index that has been queueing since before
	// lanes existed has rows with an empty lane, and no lane claims those.
	for _, name := range reg.Names() {
		st, err := reg.Existing(name)
		if err != nil {
			continue
		}
		if n, err := st.BackfillLanes(); err == nil && n > 0 {
			log.Printf("raglit: queue %s: assigned a lane to %d job(s) queued before lanes existed", name, n)
		}
	}
	var wg sync.WaitGroup
	for lane, slots := range raglit.DefaultLaneSlots {
		wg.Add(1)
		go func(lane raglit.Lane, slots int) {
			defer wg.Done()
			runLane(ctx, reg, lane, slots, func(st *raglit.Store) *raglit.Worker {
				return buildWorker(st, lf, home, pool)
			})
		}(lane, slots)
	}
	wg.Wait()
}

// runLane drains ONE lane across every index: a single claimer feeding `slots`
// runners.
//
// One claimer, not one per slot. The claim decides FAIRNESS — it is the thing
// that walks the indexes in turn — and several claimers racing the same
// round-robin cursor would hand out whatever they happened to win rather than
// what is next. Claiming is a transaction and takes microseconds, so one is not
// a bottleneck; running is minutes, which is what the slots are for.
//
// The runners each keep their OWN worker cache. A Worker carries the retry tally
// for the job in flight (Worker.Retries), so two goroutines sharing one would
// attribute an endpoint's backpressure to whichever document finished second.
// newWorker builds the worker for one index. A function rather than the flags
// themselves so a test can supply a worker whose fetch it controls, and so
// assert the property this whole change exists for: that a slow heavy job does
// not hold up a light one.
func runLane(ctx context.Context, reg *raglit.Registry, lane raglit.Lane, slots int,
	newWorker func(*raglit.Store) *raglit.Worker) {
	if slots < 1 {
		slots = 1
	}
	type claimed struct {
		index string
		job   *raglit.Job
	}
	ch := make(chan claimed)

	// A token per slot, taken BEFORE claiming and returned when the job is done.
	//
	// The claim is what writes `running` to the row, so claiming ahead of a free
	// runner makes the queue report work that nothing is doing — which is the
	// same lie owner_pid exists to expose, told by the scheduler itself. Observed
	// before this: the heavy lane has one slot, ran one job, and reported two
	// running, because the claimer had already taken the next one and was blocked
	// handing it over.
	//
	// Gating the CLAIM rather than the hand-off also means a job stays `pending`
	// — cancellable, visible as waiting — until something is actually free to run
	// it.
	free := make(chan struct{}, slots)
	for i := 0; i < slots; i++ {
		free <- struct{}{}
	}

	var wg sync.WaitGroup
	for i := 0; i < slots; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workers := map[string]*raglit.Worker{}
			for c := range ch {
				st, err := reg.Existing(c.index)
				if err == nil {
					w := workers[c.index]
					if w == nil {
						w = newWorker(st)
						workers[c.index] = w
					}
					if err := w.ProcessJob(ctx, c.job); err != nil {
						log.Printf("raglit: queue %s [%s]: job %d: %v", c.index, lane, c.job.ID, err)
					}
				}
				free <- struct{}{}
			}
		}()
	}

	// The claimer. Round-robin across indexes, resuming where the last pass
	// stopped rather than restarting at the first name: starting over every time
	// would let the alphabetically-first index with a full queue take every turn
	// while the others waited, which is the unfairness this whole change is
	// about, merely moved from between lanes to within one.
	cursor := 0
	for ctx.Err() == nil {
		select {
		case <-free:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		names := reg.Names()
		got := false
		for range names {
			if ctx.Err() != nil || len(names) == 0 {
				break
			}
			name := names[cursor%len(names)]
			cursor++
			st, err := reg.Existing(name)
			if err != nil {
				continue
			}
			job, err := st.ClaimNextInLane(lane)
			if err != nil || job == nil {
				continue
			}
			select {
			case ch <- claimed{index: name, job: job}:
				got = true
			case <-ctx.Done():
				// Claimed and never handed off. Put it back rather than leaving a
				// row that says running with nothing running it.
				_ = st.RequeueJob(job.ID)
				free <- struct{}{}
			}
			break
		}
		if !got {
			// Nothing anywhere in this lane: hand the slot back and wait.
			free <- struct{}{}
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	close(ch)
	wg.Wait()
}

// selectIndexes resolves the `index` argument to a concrete set of names: empty
// → all; else the comma-separated list. A member ending in "*" is a prefix
// wildcard, expanded against the existing indexes — clients pass "<project>__*"
// to scope "all" to their own namespace. Non-wildcard names pass through (an
// unknown one is opened empty → no hits).
func selectIndexes(reg *raglit.Registry, arg string) []string {
	arg = strings.TrimSpace(arg)
	if arg == "" || arg == "all" {
		return reg.Names()
	}
	var out []string
	for _, n := range strings.Split(arg, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if prefix, ok := strings.CutSuffix(n, "*"); ok {
			for _, nm := range reg.Names() {
				if strings.HasPrefix(nm, prefix) {
					out = append(out, nm)
				}
			}
			continue
		}
		out = append(out, n)
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
		path := req.GetString("path", "")
		names := selectIndexes(reg, req.GetString("index", ""))

		// Search each selected index; over-fetch, then RRF-merge across indexes.
		lists := map[string][]raglit.Hit{}
		for _, name := range names {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			hits, err := st.SearchPath(q, path, limit*2)
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

// taggedFigure is a figure hit tagged with the index it came from.
type taggedFigure struct {
	Index string `json:"index"`
	raglit.FigureHit
}

func searchFiguresHandler(reg *raglit.Registry, defLimit int) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query is required"), nil
		}
		limit := req.GetInt("limit", defLimit)
		path := req.GetString("path", "")
		var all []taggedFigure
		for _, name := range selectIndexes(reg, req.GetString("index", "")) {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			figs, err := st.SearchFiguresPath(ctx, q, path, limit)
			if err != nil {
				// No embedder → skip this index rather than failing the whole call.
				continue
			}
			for _, f := range figs {
				all = append(all, taggedFigure{Index: name, FigureHit: f})
			}
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
		if len(all) > limit {
			all = all[:limit]
		}
		if all == nil {
			all = []taggedFigure{}
		}
		b, err := json.Marshal(map[string]any{"figures": all})
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
		agg := raglit.NewStatus()
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

// listDocumentsHandler lists documents across the selected indexes, filtered by
// an optional case-insensitive name substring, each tagged with its index.
func listDocumentsHandler(reg *raglit.Registry) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := strings.ToLower(strings.TrimSpace(req.GetString("name", "")))
		type docOut struct {
			Index     string `json:"index"`
			Path      string `json:"path"`
			Title     string `json:"title"`
			Fragments int    `json:"fragments"`
			Pages     int    `json:"pages"`
			Vision    int    `json:"vision"`
			FragMode  string `json:"frag_mode"`
		}
		out := struct {
			Documents []docOut `json:"documents"`
		}{Documents: []docOut{}}
		for _, idx := range selectIndexes(reg, req.GetString("index", "")) {
			st, err := reg.Get(idx)
			if err != nil {
				continue
			}
			docs, err := st.Documents()
			if err != nil {
				return mcp.NewToolResultErrorFromErr("list_documents", err), nil
			}
			for _, d := range docs {
				if name != "" && !strings.Contains(strings.ToLower(d.Path), name) &&
					!strings.Contains(strings.ToLower(d.Title), name) {
					continue
				}
				out.Documents = append(out.Documents, docOut{
					Index: idx, Path: d.Path, Title: d.Title,
					Fragments: d.Fragments, Pages: d.Pages, Vision: d.Vision, FragMode: d.FragMode,
				})
			}
		}
		b, err := json.Marshal(out)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode", err), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

// getDocumentHandler returns a document's indexed text. It resolves `path` (an
// exact path or a unique filename substring) across the selected indexes: no
// match → error, multiple → an ambiguity error listing candidates, one → its
// reassembled text (optionally page-ranged and length-capped).
func getDocumentHandler(reg *raglit.Registry) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError("path is required"), nil
		}
		// Resolve candidates across the selected indexes.
		type cand struct{ index, path, title string }
		var cands []cand
		for _, idx := range selectIndexes(reg, req.GetString("index", "")) {
			st, err := reg.Get(idx)
			if err != nil {
				continue
			}
			ms, err := st.MatchDocuments(ref)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("resolve", err), nil
			}
			for _, m := range ms {
				cands = append(cands, cand{idx, m.Path, m.Title})
			}
		}
		if len(cands) == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("no document matches %q", ref)), nil
		}
		if len(cands) > 1 {
			var b strings.Builder
			fmt.Fprintf(&b, "%q is ambiguous — matches %d documents:\n", ref, len(cands))
			for i, c := range cands {
				if i == 8 {
					fmt.Fprintf(&b, "  … and %d more\n", len(cands)-8)
					break
				}
				fmt.Fprintf(&b, "  [%s] %s\n", c.index, c.path)
			}
			b.WriteString("pass a more specific path (or set index).")
			return mcp.NewToolResultError(b.String()), nil
		}

		c := cands[0]
		from, to := req.GetInt("from", 0), req.GetInt("to", 0)
		if p := req.GetInt("page", 0); p > 0 {
			from, to = p, p
		}
		st, err := reg.Get(c.index)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("open index", err), nil
		}
		content, err := st.DocText(c.path, from, to, req.GetInt("max_chars", 0))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("get_document", err), nil
		}
		payload := struct {
			Index string `json:"index"`
			raglit.DocContent
		}{Index: c.index, DocContent: content}
		b, err := json.Marshal(payload)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode", err), nil
		}
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
		// Origin marks a hit on GENERATED text — the document's caption and
		// summary (identity.go), not its own words. It ranks in the same list
		// because that is what makes a badly named document findable, and it is
		// labelled because an agent quoting it would be quoting a paraphrase.
		Origin string `json:"origin,omitempty"`
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
			Score: h.Score, Snippet: clip(oneLine(h.Text), 300), Origin: h.Origin,
		})
	}
	return out
}
