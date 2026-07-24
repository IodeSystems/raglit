package main

// gat multi-protocol daemon (P2). The raglit daemon rebuilt on the huma+gwag/gat
// stack: every JSON operation is a gat.Register call, so it's served as REST +
// in-process GraphQL + gRPC off one port, with OpenAPI at /openapi.json. Handlers
// call the existing Store/Registry (the sqlc/metaquery migration of Store's guts
// is P3 — under this HTTP layer, no handler change). The HTML review UI and the
// binary page-image are plain chi routes (not JSON ops).
//
// Runs alongside the legacy stdlib `daemon`/`review` until it reaches parity;
// then those switch over. See plan/daemon-stack.md.

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/iodesystems/gwag/gw/gat"
	"github.com/iodesystems/raglit"
)

func runHttpd(args []string) error {
	fs := flag.NewFlagSet("httpd", flag.ExitOnError)
	homeFlag := fs.String("home", "", "single-home index dir (back-compat; overrides --root)")
	rootFlag := fs.String("root", "", "scoped storage root (default $RAGLIT_ROOT or ~/.raglit); each index at <root>/indexes/<name>")
	lf := addLLMFlags(fs)
	addr := fs.String("addr", defaultDaemonAddr, "listen address")
	defLimit := fs.Int("n", 8, "default search results")
	embed := fs.Bool("embed", false, "embed ingested fragments (enables vector search)")
	poolTTL := fs.Duration("pool-ttl", 720*time.Hour, "evict pooled docs unused this long (0 = never)")
	poolMax := fs.Int("pool-max", 0, "cap the shared pool at N entries, LRU-evicting the rest (0 = unlimited)")
	fs.Parse(args)

	reg, cfgHome, err := openDaemonRegistry(*homeFlag, *rootFlag)
	if err != nil {
		return err
	}
	defer reg.Close()
	lf.resolve(cfgHome) // daemon config (endpoint + models) from the home / root
	if *embed {
		if err := lf.requireEmbed(); err != nil {
			return err
		}
		reg.SetEmbedder(lf.embedder())
	}
	// Shared document pool: ingest work (extract/OCR/segment/embed) is cached by
	// (recipe, file hash) under the daemon's storage root, so the same file — in
	// ANY index, or on a retry — is reused instead of reprocessed.
	pool, err := raglit.OpenPool(string(cfgHome))
	if err != nil {
		return err
	}
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runIndexWorkers(ctx, reg, lf, cfgHome, pool)

	// Background pool GC: evict unused/over-cap entries hourly.
	if *poolTTL > 0 || *poolMax > 0 {
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if n, err := pool.GC(*poolTTL, *poolMax); err == nil && n > 0 {
						fmt.Fprintf(os.Stderr, "raglit: pool GC evicted %d entr(ies)\n", n)
					}
				}
			}
		}()
	}

	handler, err := buildGatHandler(reg, lf, cfgHome, *defLimit, pool, *poolTTL, *poolMax)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "raglit httpd (gat) on http://%s (storage %s)\n", *addr, cfgHome)
	fmt.Fprintf(os.Stderr, "  REST + review UI: http://%s/   OpenAPI: /openapi.json   GraphQL: /graphql\n", *addr)
	return (&http.Server{Addr: *addr, Handler: handler}).ListenAndServe()
}

// openDaemonRegistry opens the daemon's index registry: an explicit --home is the
// single-home layout (back-compat, e.g. a project .raglit/); otherwise scoped
// storage under --root (default DefaultRoot()), where each index is its own Home
// at <root>/indexes/<name>. Returns the registry plus the Home to read the
// daemon's own config (endpoint + models) from — the home, or <root>/config.json.
func openDaemonRegistry(homeFlag, rootFlag string) (*raglit.Registry, raglit.Home, error) {
	if homeFlag != "" {
		reg, err := raglit.OpenRegistry(raglit.Home(homeFlag))
		return reg, raglit.Home(homeFlag), err
	}
	root := rootFlag
	if root == "" {
		root = raglit.DefaultRoot()
	}
	reg, err := raglit.OpenScopedRegistry(root)
	return reg, raglit.Home(root), err
}

// buildGatHandler wires the chi router: humachi API + gat gateway (all JSON
// operations), then the two plain routes (HTML UI at /, binary page-image).
func buildGatHandler(reg *raglit.Registry, lf *llmFlags, home raglit.Home, defLimit int, pool *raglit.Pool, gcTTL time.Duration, gcMax int) (http.Handler, error) {
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("raglit", version))
	g, err := gat.New()
	if err != nil {
		return nil, err
	}

	op := func(id, method, path, summary string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary}
	}
	gat.Register(api, g, op("health", http.MethodGet, "/api/health", "Liveness probe."), health)
	gat.Register(api, g, op("listIndexes", http.MethodGet, "/indexes", "List indexes with doc/fragment counts."), listIndexes(reg))
	gat.Register(api, g, op("status", http.MethodGet, "/status", "Index + ingest-queue status (aggregate or one index)."), statusOp(reg))
	gat.Register(api, g, op("search", http.MethodGet, "/search", "Search index(es); RRF-merged, best first."), searchOp(reg, defLimit))
	gat.Register(api, g, op("ingest", http.MethodPost, "/ingest", "Queue targets for lazy ingestion."), ingestOp(reg))
	gat.Register(api, g, op("listJobs", http.MethodGet, "/api/jobs", "List ingest jobs (all states) with stages + ETA."), listJobs(reg))
	gat.Register(api, g, op("retryJob", http.MethodPost, "/api/jobs/retry", "Requeue an errored/done job."), jobAction(reg, (*raglit.Store).RetryJob))
	gat.Register(api, g, op("cancelJob", http.MethodPost, "/api/jobs/cancel", "Cancel a pending job."), jobAction(reg, (*raglit.Store).CancelJob))
	gat.Register(api, g, op("listDocuments", http.MethodGet, "/api/documents", "List documents with fragment/page/engine counts."), documentsOp(reg))
	gat.Register(api, g, op("getDocReview", http.MethodGet, "/api/doc", "Per-page OCR review for a document."), docReviewOp(reg))
	gat.Register(api, g, op("reocr", http.MethodPost, "/api/reocr", "Re-run the OCR cascade on a saved page image."), reocrOp(reg, lf, home))
	gat.Register(api, g, op("findDocuments", http.MethodGet, "/api/find-documents", "Find documents by name substring (MCP list_documents)."), findDocumentsOp(reg))
	gat.Register(api, g, op("getDocument", http.MethodGet, "/api/get-document", "Get a document's indexed text (MCP get_document)."), getDocumentOp(reg))
	gat.Register(api, g, op("ocr", http.MethodPost, "/api/ocr", "Extract a document (path or base64 data) to paged text (MCP ocr)."), ocrToolOp(lf, home))
	gat.Register(api, g, op("listBranches", http.MethodGet, "/api/branches", "List branches: lineage, age, last-access, local doc count."), listBranchesOp(reg))
	gat.Register(api, g, op("forkBranch", http.MethodPost, "/api/branches", "Fork a branch off a parent index (copy-on-write overlay)."), forkBranchOp(reg))
	gat.Register(api, g, op("deleteBranch", http.MethodDelete, "/api/branches", "Delete a branch (its storage); parent untouched."), deleteBranchOp(reg))
	if pool != nil {
		gat.Register(api, g, op("poolStats", http.MethodGet, "/api/pool", "Shared document-pool size (entries + files)."), poolStatsOp(pool))
		gat.Register(api, g, op("poolGC", http.MethodPost, "/api/pool/gc", "Evict pooled docs (unused past max_age_hours, or over max_entries LRU)."), poolGCOp(pool, gcTTL, gcMax))
	}

	if err := gat.RegisterHuma(api, g, ""); err != nil {
		return nil, err
	}

	// Plain routes (not JSON operations): the self-contained HTML UI + the binary
	// page image. Served directly on the router alongside the gat-mounted ops.
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(reviewHTML)
	})
	router.Get("/api/page-image", func(w http.ResponseWriter, r *http.Request) {
		st, err := reg.Get(r.URL.Query().Get("index"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		img, err := st.PageImagePath(r.URL.Query().Get("path"), queryInt(r, "page", 0))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if root := st.PagesRoot(); img == "" || root == "" || !isUnder(img, root) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, img)
	})
	return router, nil
}

// ── operations ─────────────────────────────────────────────────────────

type healthOut struct {
	Body struct {
		Status string `json:"status"`
	}
}

func health(_ context.Context, _ *struct{}) (*healthOut, error) {
	out := &healthOut{}
	out.Body.Status = "ok"
	return out, nil
}

type idxRow struct {
	Name      string `json:"name"`
	Documents int    `json:"documents"`
	Fragments int    `json:"fragments"`
}
type listIndexesOut struct {
	Body struct {
		Indexes []idxRow `json:"indexes"`
	}
}

func listIndexes(reg *raglit.Registry) func(context.Context, *struct{}) (*listIndexesOut, error) {
	return func(_ context.Context, _ *struct{}) (*listIndexesOut, error) {
		out := &listIndexesOut{}
		out.Body.Indexes = []idxRow{}
		for _, name := range reg.Names() {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			s, _ := st.IndexStatus()
			out.Body.Indexes = append(out.Body.Indexes, idxRow{name, s.Documents, s.Fragments})
		}
		return out, nil
	}
}

type statusIn struct {
	Index string `query:"index"`
}
type statusOut struct {
	Body raglit.Status
}

func statusOp(reg *raglit.Registry) func(context.Context, *statusIn) (*statusOut, error) {
	return func(_ context.Context, in *statusIn) (*statusOut, error) {
		return &statusOut{Body: aggregateStatus(reg, selectIndexes(reg, in.Index))}, nil
	}
}

type hitRow struct {
	Index   string  `json:"index"`
	DocID   string  `json:"doc_id"`
	Title   string  `json:"title"`
	Page    int     `json:"page"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}
type searchIn struct {
	Query string `query:"q"`
	Index string `query:"index"`
	Mode  string `query:"mode"`
	Limit int    `query:"n"`
}
type searchOut struct {
	Body struct {
		Hits []hitRow `json:"hits"`
	}
}

func searchOp(reg *raglit.Registry, defLimit int) func(context.Context, *searchIn) (*searchOut, error) {
	return func(_ context.Context, in *searchIn) (*searchOut, error) {
		if in.Query == "" {
			return nil, huma.Error400BadRequest("q is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = defLimit
		}
		lists := map[string][]raglit.Hit{}
		for _, name := range selectIndexes(reg, in.Index) {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			hits, err := searchByMode(st, in.Query, in.Mode, limit*2)
			if err != nil {
				return nil, huma.Error500InternalServerError("search", err)
			}
			lists[name] = hits
		}
		out := &searchOut{}
		out.Body.Hits = []hitRow{}
		for _, ih := range rrfMerge(lists, limit) {
			h := ih.hit
			title := h.Title
			if title == "" {
				title = h.Path
			}
			out.Body.Hits = append(out.Body.Hits, hitRow{
				Index: ih.index, DocID: h.Path, Title: title, Page: h.Page,
				Score: h.Score, Snippet: clip(oneLine(h.Text), 300),
			})
		}
		return out, nil
	}
}

type ingestIn struct {
	Body struct {
		Targets []string `json:"targets"`
		Index   string   `json:"index,omitempty"`
		Title   string   `json:"title,omitempty"`
	}
}
type ingestOut struct {
	Body struct {
		Queued int     `json:"queued"`
		JobIDs []int64 `json:"job_ids"`
		Index  string  `json:"index"`
	}
}

func ingestOp(reg *raglit.Registry) func(context.Context, *ingestIn) (*ingestOut, error) {
	return func(_ context.Context, in *ingestIn) (*ingestOut, error) {
		st, err := reg.Get(in.Body.Index)
		if err != nil {
			return nil, huma.Error500InternalServerError("open index", err)
		}
		ids := make([]int64, 0, len(in.Body.Targets))
		for _, t := range in.Body.Targets {
			id, err := st.Enqueue(t, in.Body.Title)
			if err != nil {
				return nil, huma.Error500InternalServerError("enqueue", err)
			}
			ids = append(ids, id)
		}
		out := &ingestOut{}
		out.Body.Queued = len(ids)
		out.Body.JobIDs = ids
		out.Body.Index = defaultIndexName(in.Body.Index)
		return out, nil
	}
}

type jobOut struct {
	raglit.JobInfo
	ETASeconds float64           `json:"eta_seconds"`
	Stages     []raglit.JobStage `json:"stages"`
}
type jobsIn struct {
	Index string `query:"index"`
	State string `query:"state"`
	Limit int    `query:"limit"`
}
type jobsOut struct {
	Body struct {
		Jobs []jobOut `json:"jobs"`
	}
}

func listJobs(reg *raglit.Registry) func(context.Context, *jobsIn) (*jobsOut, error) {
	return func(_ context.Context, in *jobsIn) (*jobsOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound(err.Error())
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 100
		}
		jobs, err := st.Jobs(in.State, limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("jobs", err)
		}
		snap, _ := st.IndexStatus()
		eta := map[int64]float64{}
		for _, it := range snap.Items {
			eta[it.ID] = it.ETASeconds
		}
		out := &jobsOut{}
		out.Body.Jobs = []jobOut{}
		for _, j := range jobs {
			stages, _ := st.JobStages(j.ID)
			if stages == nil {
				stages = []raglit.JobStage{}
			}
			out.Body.Jobs = append(out.Body.Jobs, jobOut{JobInfo: j, ETASeconds: eta[j.ID], Stages: stages})
		}
		return out, nil
	}
}

type jobActionIn struct {
	Index string `query:"index"`
	Body  struct {
		ID int64 `json:"id"`
	}
}
type okOut struct {
	Body struct {
		OK bool  `json:"ok"`
		ID int64 `json:"id"`
	}
}

func jobAction(reg *raglit.Registry, action func(*raglit.Store, int64) error) func(context.Context, *jobActionIn) (*okOut, error) {
	return func(_ context.Context, in *jobActionIn) (*okOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound(err.Error())
		}
		if err := action(st, in.Body.ID); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := &okOut{}
		out.Body.OK = true
		out.Body.ID = in.Body.ID
		return out, nil
	}
}

type documentsIn struct {
	Index string `query:"index"`
}
type documentsOut struct {
	Body struct {
		Documents []raglit.DocSummary `json:"documents"`
	}
}

func documentsOp(reg *raglit.Registry) func(context.Context, *documentsIn) (*documentsOut, error) {
	return func(_ context.Context, in *documentsIn) (*documentsOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound(err.Error())
		}
		docs, err := st.Documents()
		if err != nil {
			return nil, huma.Error500InternalServerError("documents", err)
		}
		if docs == nil {
			docs = []raglit.DocSummary{}
		}
		out := &documentsOut{}
		out.Body.Documents = docs
		return out, nil
	}
}

type docReviewIn struct {
	Index string `query:"index"`
	Path  string `query:"path"`
}
type docReviewOut struct {
	Body struct {
		Path  string              `json:"path"`
		Title string              `json:"title"`
		Pages []raglit.PageReview `json:"pages"`
	}
}

func docReviewOp(reg *raglit.Registry) func(context.Context, *docReviewIn) (*docReviewOut, error) {
	return func(_ context.Context, in *docReviewIn) (*docReviewOut, error) {
		if in.Path == "" {
			return nil, huma.Error400BadRequest("path is required")
		}
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound(err.Error())
		}
		title, pages, err := st.DocReview(in.Path)
		if err != nil {
			return nil, huma.Error500InternalServerError("doc", err)
		}
		if pages == nil {
			pages = []raglit.PageReview{}
		}
		out := &docReviewOut{}
		out.Body.Path, out.Body.Title, out.Body.Pages = in.Path, title, pages
		return out, nil
	}
}

type reocrIn struct {
	Index string `query:"index"`
	Body  struct {
		Path string `json:"path"`
		Page int    `json:"page"`
	}
}
type reocrOut struct {
	Body struct {
		Page   int    `json:"page"`
		Engine string `json:"engine"`
		Text   string `json:"text"`
	}
}

func reocrOp(reg *raglit.Registry, lf *llmFlags, home raglit.Home) func(context.Context, *reocrIn) (*reocrOut, error) {
	return func(ctx context.Context, in *reocrIn) (*reocrOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound(err.Error())
		}
		img, err := st.PageImagePath(in.Body.Path, in.Body.Page)
		if err != nil || img == "" || !isUnder(img, st.PagesRoot()) {
			return nil, huma.Error404NotFound("no saved page image for that page")
		}
		data, err := os.ReadFile(img)
		if err != nil {
			return nil, huma.Error500InternalServerError("read image", err)
		}
		text, engine, err := buildToolOCR(lf, home).PageWithEngine(ctx, raglit.PageImage{
			Page: in.Body.Page, Mime: mimeForImage(img), Data: data,
		})
		if err != nil {
			return nil, huma.Error500InternalServerError("reocr", err)
		}
		out := &reocrOut{}
		out.Body.Page, out.Body.Engine, out.Body.Text = in.Body.Page, engine, text
		return out, nil
	}
}

type findDocRow struct {
	Index     string `json:"index"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	Fragments int    `json:"fragments"`
	Pages     int    `json:"pages"`
	Vision    int    `json:"vision"`
}
type findDocumentsIn struct {
	Name  string `query:"name"`
	Index string `query:"index"`
}
type findDocumentsOut struct {
	Body struct {
		Documents []findDocRow `json:"documents"`
	}
}

func findDocumentsOp(reg *raglit.Registry) func(context.Context, *findDocumentsIn) (*findDocumentsOut, error) {
	return func(_ context.Context, in *findDocumentsIn) (*findDocumentsOut, error) {
		out := &findDocumentsOut{}
		out.Body.Documents = []findDocRow{}
		name := strings.ToLower(strings.TrimSpace(in.Name))
		for _, idx := range selectIndexes(reg, in.Index) {
			st, err := reg.Get(idx)
			if err != nil {
				continue
			}
			docs, err := st.Documents()
			if err != nil {
				return nil, huma.Error500InternalServerError("find", err)
			}
			for _, d := range docs {
				if name != "" && !strings.Contains(strings.ToLower(d.Path), name) && !strings.Contains(strings.ToLower(d.Title), name) {
					continue
				}
				out.Body.Documents = append(out.Body.Documents, findDocRow{
					Index: idx, Path: d.Path, Title: d.Title,
					Fragments: d.Fragments, Pages: d.Pages, Vision: d.Vision,
				})
			}
		}
		return out, nil
	}
}

type getDocumentIn struct {
	Path     string `query:"path"`
	Page     int    `query:"page"`
	From     int    `query:"from"`
	To       int    `query:"to"`
	MaxChars int    `query:"max_chars"`
	Index    string `query:"index"`
}
type getDocumentOut struct {
	Body struct {
		Index string `json:"index"`
		raglit.DocContent
	}
}

func getDocumentOp(reg *raglit.Registry) func(context.Context, *getDocumentIn) (*getDocumentOut, error) {
	return func(_ context.Context, in *getDocumentIn) (*getDocumentOut, error) {
		if in.Path == "" {
			return nil, huma.Error400BadRequest("path is required")
		}
		type cand struct{ index, path string }
		var cands []cand
		for _, idx := range selectIndexes(reg, in.Index) {
			st, err := reg.Get(idx)
			if err != nil {
				continue
			}
			ms, err := st.MatchDocuments(in.Path)
			if err != nil {
				return nil, huma.Error500InternalServerError("resolve", err)
			}
			for _, m := range ms {
				cands = append(cands, cand{idx, m.Path})
			}
		}
		if len(cands) == 0 {
			return nil, huma.Error404NotFound(fmt.Sprintf("no document matches %q", in.Path))
		}
		if len(cands) > 1 {
			return nil, huma.Error409Conflict(fmt.Sprintf("%q is ambiguous — matches %d documents; pass a more specific path or set index", in.Path, len(cands)))
		}
		from, to := in.From, in.To
		if in.Page > 0 {
			from, to = in.Page, in.Page
		}
		st, err := reg.Get(cands[0].index)
		if err != nil {
			return nil, huma.Error500InternalServerError("open index", err)
		}
		content, err := st.DocText(cands[0].path, from, to, in.MaxChars)
		if err != nil {
			return nil, huma.Error500InternalServerError("get_document", err)
		}
		out := &getDocumentOut{}
		out.Body.Index, out.Body.DocContent = cands[0].index, content
		return out, nil
	}
}

type poolStatsOut struct {
	Body raglit.PoolStats
}

func poolStatsOp(pool *raglit.Pool) func(context.Context, *struct{}) (*poolStatsOut, error) {
	return func(_ context.Context, _ *struct{}) (*poolStatsOut, error) {
		st, err := pool.Stats()
		if err != nil {
			return nil, huma.Error500InternalServerError("pool", err)
		}
		return &poolStatsOut{Body: st}, nil
	}
}

type poolGCIn struct {
	Body struct {
		MaxAgeHours float64 `json:"max_age_hours,omitempty"`
		MaxEntries  int     `json:"max_entries,omitempty"`
	}
}
type poolGCOut struct {
	Body struct {
		Evicted int `json:"evicted"`
	}
}

// poolGCOp runs pool eviction, defaulting to the daemon's --pool-ttl/--pool-max
// when the request omits them.
func poolGCOp(pool *raglit.Pool, defTTL time.Duration, defMax int) func(context.Context, *poolGCIn) (*poolGCOut, error) {
	return func(_ context.Context, in *poolGCIn) (*poolGCOut, error) {
		ttl, max := defTTL, defMax
		if in.Body.MaxAgeHours > 0 {
			ttl = time.Duration(in.Body.MaxAgeHours * float64(time.Hour))
		}
		if in.Body.MaxEntries > 0 {
			max = in.Body.MaxEntries
		}
		n, err := pool.GC(ttl, max)
		if err != nil {
			return nil, huma.Error500InternalServerError("pool gc", err)
		}
		out := &poolGCOut{}
		out.Body.Evicted = n
		return out, nil
	}
}

type listBranchesOut struct {
	Body struct {
		Branches []raglit.BranchInfo `json:"branches"`
	}
}

func listBranchesOp(reg *raglit.Registry) func(context.Context, *struct{}) (*listBranchesOut, error) {
	return func(_ context.Context, _ *struct{}) (*listBranchesOut, error) {
		bs, err := reg.ListBranches()
		if err != nil {
			return nil, huma.Error500InternalServerError("branches", err)
		}
		if bs == nil {
			bs = []raglit.BranchInfo{}
		}
		out := &listBranchesOut{}
		out.Body.Branches = bs
		return out, nil
	}
}

type forkBranchIn struct {
	Body struct {
		Name   string `json:"name"`
		Parent string `json:"parent,omitempty"`
	}
}
type forkBranchOut struct {
	Body struct {
		OK     bool   `json:"ok"`
		Name   string `json:"name"`
		Parent string `json:"parent"`
	}
}

func forkBranchOp(reg *raglit.Registry) func(context.Context, *forkBranchIn) (*forkBranchOut, error) {
	return func(_ context.Context, in *forkBranchIn) (*forkBranchOut, error) {
		parent := in.Body.Parent
		if parent == "" {
			parent = "default"
		}
		if err := reg.ForkBranch(in.Body.Name, parent); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := &forkBranchOut{}
		out.Body.OK, out.Body.Name, out.Body.Parent = true, in.Body.Name, parent
		return out, nil
	}
}

type deleteBranchIn struct {
	Name string `query:"name"`
}

func deleteBranchOp(reg *raglit.Registry) func(context.Context, *deleteBranchIn) (*okOut, error) {
	return func(_ context.Context, in *deleteBranchIn) (*okOut, error) {
		if err := reg.DeleteBranch(in.Name); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

type ocrToolIn struct {
	Body struct {
		Path string `json:"path,omitempty"`
		Data string `json:"data,omitempty"`
		Mime string `json:"mime,omitempty"`
	}
}
type ocrToolOut struct {
	Body ocrOut
}

// ocrToolOp is the daemon side of the MCP `ocr` tool: resolve a path/base64 doc
// to a temp file, run the format router + OCR cascade, return paged text.
func ocrToolOp(lf *llmFlags, home raglit.Home) func(context.Context, *ocrToolIn) (*ocrToolOut, error) {
	return func(ctx context.Context, in *ocrToolIn) (*ocrToolOut, error) {
		fp, cleanup, err := resolveDoc(in.Body.Path, in.Body.Data, in.Body.Mime)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		defer cleanup()
		res, err := ocrDocument(ctx, buildToolOCR(lf, home), fp)
		if err != nil {
			return nil, huma.Error500InternalServerError("ocr", err)
		}
		return &ocrToolOut{Body: res}, nil
	}
}

// defaultIndexName echoes the requested index for an output, defaulting empty to
// "default" (matching reg.Get's normalization for the common case).
func defaultIndexName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "default"
	}
	return name
}
