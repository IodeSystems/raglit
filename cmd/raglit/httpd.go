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
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/iodesystems/gwag/gw/gat"
	"github.com/iodesystems/raglit"
)

func runHttpd(subcmd string, args []string) error {
	fs := flag.NewFlagSet("httpd", flag.ExitOnError)
	homeFlag := fs.String("home", "", "single-home index dir (back-compat; overrides --root)")
	rootFlag := fs.String("root", "", "scoped storage root (default $RAGLIT_ROOT or ~/.raglit); each index at <root>/indexes/<name>")
	lf := addLLMFlags(fs)
	addr := fs.String("addr", defaultDaemonAddr, "listen address(es), comma-separated; a bare `.N` matches this host's own interface ending .N (e.g. 127.0.0.1:7420,.76)")
	defLimit := fs.Int("n", 8, "default search results")
	embed := fs.Bool("embed", false, "embed ingested fragments (enables vector search)")
	poolMaxBytes := fs.Int64("pool-max-bytes", 4<<30, "keep the shared pool under this many bytes, evicting oldest-accessed (0 = unlimited)")
	poolMax := fs.Int("pool-max", 0, "also cap the pool at N entries, LRU (0 = unlimited)")
	poolTTL := fs.Duration("pool-ttl", 0, "also evict pooled docs unused this long (0 = never — off so merges/retries keep reusing)")
	stop := fs.Bool("stop", false, "signal the running daemon (recorded in <root>/daemon.json) to shut down, then exit")
	restart := fs.Bool("restart", false, "stop the running daemon, then relaunch it detached with these flags (picks up a rebuilt binary), and exit")
	watchInterval := fs.Duration("watch-interval", 5*time.Second, "how often to re-scan watched projects for changes")
	check := fs.Bool("check", false, "parse these flags and exit; nothing is started, nothing is opened")
	fs.Parse(args)

	// --check: prove a set of daemon flags is valid without starting anything.
	//
	// This exists for `raglit service install`, which writes those flags into a
	// systemd unit. A unit whose ExecStart has a bad flag does not fail once —
	// it fails, gets restarted, and fails again, so a typo becomes a crash loop
	// instead of an error message. (That is not hypothetical: it took a sibling
	// service down for three minutes and 138 restarts.) Validating through the
	// REAL flag set, rather than a copy of it, is the only version that cannot
	// drift away from what the daemon actually accepts.
	if *check {
		return nil
	}

	// --stop / --restart: act on the daemon recorded under this root and return
	// (no server in THIS process — restart relaunches a detached one).
	if *stop || *restart {
		root := *rootFlag
		if root == "" {
			root = raglit.DefaultRoot()
		}
		// Refuse to step around a supervisor. --restart stops the daemon and
		// relaunches it DETACHED, so under a systemd unit it silently swaps a
		// supervised daemon for one systemd does not know about: the unit goes
		// inactive (a clean exit, so Restart=on-failure correctly does nothing)
		// and the replacement will not survive a reboot. That is the exact
		// problem `raglit service` exists to fix, undone by the command people
		// reach for after a rebuild.
		if st, ok := readDaemonState(root); ok && st != nil && st.Unit != "" {
			verb := "stop"
			if *restart {
				verb = "restart"
			}
			return fmt.Errorf("that daemon is supervised by %s — use `raglit service %s`\n"+
				"  (--%s would leave systemd with nothing to supervise)", st.Unit, verb, verb)
		}
		if *stop {
			return stopDaemon(root)
		}
		return restartDaemon(root, subcmd, args, *addr)
	}

	reg, cfgHome, err := openDaemonRegistry(*homeFlag, *rootFlag)
	if err != nil {
		return err
	}
	defer reg.Close()
	lf.resolve(cfgHome) // daemon config (endpoint + models) from the home / root
	// Embed automatically when the config has an embed model (so an auto-started
	// daemon supports vector/hybrid search); --embed with no model is an error.
	if *lf.embedModel != "" {
		reg.SetEmbedder(lf.embedder())
	} else if *embed {
		return lf.requireEmbed()
	}
	// Optional figure IMAGE embedder (nomic-vision); nil → figures embed descriptions.
	if ie := buildImageEmbedder(cfgHome); ie != nil {
		reg.SetImageEmbedder(ie)
	}
	// What each ingested document IS (identity.go). Set on the REGISTRY as well
	// as on each worker's store, because /api/identify captions an already-
	// indexed corpus and runs on a store the ingest path may never have touched.
	reg.SetIdentifier(lf.identifier(cfgHome))
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
	// Captions, on their own queue and their own slot budget. See runIdentityWorkers.
	go runIdentityWorkers(ctx, reg, cfgHome)
	// Stay on the current source. Rebuilds immediately, restarts as soon as no
	// job is RUNNING — a re-exec mid-ingest aborts that job and it is not
	// requeued, while pending rows survive and the new process picks them up.
	go daemonSelfUpdate(func() bool { return noJobRunning(reg) })

	// Directory watching: keep opt-in projects (config watch:true) re-ingested on
	// change. Registrations persist under the daemon root and reload here.
	watch := newWatcher(reg, string(cfgHome), *watchInterval)
	watch.load()
	go watch.run(ctx)

	// Background pool GC: keep the pool within the (lax, size-based) budget hourly.
	gcPol := raglit.GCPolicy{MaxBytes: *poolMaxBytes, MaxEntries: *poolMax, MaxAgeUnused: *poolTTL}
	if gcPol.MaxBytes > 0 || gcPol.MaxEntries > 0 || gcPol.MaxAgeUnused > 0 {
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if n, err := pool.GC(gcPol); err == nil && n > 0 {
						fmt.Fprintf(os.Stderr, "raglit: pool GC evicted %d entr(ies)\n", n)
					}
				}
			}
		}()
	}

	handler, err := buildGatHandler(reg, lf, cfgHome, *defLimit, pool, gcPol, watch)
	if err != nil {
		return err
	}
	// Record runtime state so clients discover this daemon (even on a non-default
	// port) and `--stop` can signal it. Removed on clean shutdown.
	// Record the first bind rather than the raw list: daemon.json is what a
	// client DIALS, and a comma-separated list is not an address.
	binds, err := parseListenList(*addr, defaultDaemonPort)
	if err != nil {
		return err
	}
	removeState, err := writeDaemonState(string(cfgHome), binds[0])
	if err != nil {
		return err
	}
	defer removeState()

	// One handler, several listeners. A list rather than a single bind so the
	// daemon can be reachable from the machines that should reach it without
	// being reachable from everything else — see parseListenList.
	srv := &http.Server{Handler: handler}
	// Graceful shutdown on SIGINT/SIGTERM (what `--stop` sends) so the deferred
	// state removal + pool/registry closes run.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		srv.Shutdown(sctx)
	}()

	fmt.Fprintf(os.Stderr, "raglit httpd (gat) storage %s\n", cfgHome)
	for _, b := range binds {
		fmt.Fprintf(os.Stderr, "  http://%s/   review UI · OpenAPI /openapi.json · GraphQL /graphql · review workbench /attest/<index>\n", b)
	}

	// Every listener is opened BEFORE any is served, so a partial bind fails the
	// whole start rather than leaving the daemon up on some of the addresses it
	// was told to hold. Coming up on the loopback while silently failing to bind
	// the VPN address is the failure that looks like "it works here" for hours.
	var lns []net.Listener
	for _, b := range binds {
		ln, lerr := net.Listen("tcp", b)
		if lerr != nil {
			for _, open := range lns {
				_ = open.Close()
			}
			return fmt.Errorf("listen %s: %w", b, lerr)
		}
		lns = append(lns, ln)
	}

	errc := make(chan error, len(lns))
	for _, ln := range lns {
		go func(l net.Listener) { errc <- srv.Serve(l) }(ln)
	}
	if err := <-errc; err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
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
func buildGatHandler(reg *raglit.Registry, lf *llmFlags, home raglit.Home, defLimit int, pool *raglit.Pool, defGC raglit.GCPolicy, watch *watcher) (http.Handler, error) {
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
	gat.Register(api, g, op("searchFigures", http.MethodGet, "/search-figures", "Semantic search over figures (MCP search_figures)."), searchFiguresOp(reg, defLimit))
	gat.Register(api, g, op("ingest", http.MethodPost, "/ingest", "Queue targets for lazy ingestion."), ingestOp(reg))
	gat.Register(api, g, op("listJobs", http.MethodGet, "/api/jobs", "List ingest jobs (all states) with stages + ETA."), listJobs(reg))
	gat.Register(api, g, op("retryJob", http.MethodPost, "/api/jobs/retry", "Requeue an errored/done job."), jobAction(reg, (*raglit.Store).RetryJob))
	gat.Register(api, g, op("cancelJob", http.MethodPost, "/api/jobs/cancel", "Cancel a pending job."), jobAction(reg, (*raglit.Store).CancelJob))
	gat.Register(api, g, op("forgetJob", http.MethodPost, "/api/jobs/forget", "Delete a terminal job row: that attempt is not coming back."), jobAction(reg, (*raglit.Store).ForgetJob))
	gat.Register(api, g, op("listDocuments", http.MethodGet, "/api/documents", "List documents with fragment/page/engine counts."), documentsOp(reg))
	gat.Register(api, g, op("getDocReview", http.MethodGet, "/api/doc", "Per-page OCR review for a document."), docReviewOp(reg))
	gat.Register(api, g, op("reocr", http.MethodPost, "/api/reocr", "Re-run the OCR cascade on a saved page image."), reocrOp(reg, lf, home))
	gat.Register(api, g, op("findDocuments", http.MethodGet, "/api/find-documents", "Find documents by name substring (MCP list_documents)."), findDocumentsOp(reg))
	gat.Register(api, g, op("getDocument", http.MethodGet, "/api/get-document", "Get a document's indexed text (MCP get_document)."), getDocumentOp(reg))
	gat.Register(api, g, op("ocr", http.MethodPost, "/api/ocr", "Extract a document (path or base64 data) to paged text (MCP ocr)."), ocrToolOp(lf, home))
	gat.Register(api, g, op("listRelations", http.MethodGet, "/api/relations", "Rulings on which documents are copies or versions."), listRelationsOp)
	gat.Register(api, g, op("listSlices", http.MethodGet, "/api/slices", "Declared sub-documents: page ranges of a bundle."), listSlicesOp)
	gat.Register(api, g, op("correctPage", http.MethodPost, "/api/transcribe/correct", "Record a corrected reading for one page."), correctPageOp(reg))
	gat.Register(api, g, op("sketch", http.MethodPost, "/api/similar/build", "Build page sketches for near-duplicate detection."), sketchOp(reg))
	gat.Register(api, g, op("reread", http.MethodPost, "/api/reread", "Purge a document's cached page OCR and re-read it."), rereadOp(reg))
	gat.Register(api, g, op("enqueueIdentity", http.MethodPost, "/api/identify/queue", "Queue documents for captioning; the daemon drains them at the endpoint's concurrency."), enqueueIdentityOp(reg))
	gat.Register(api, g, op("identityJobs", http.MethodGet, "/api/identity-jobs", "The captioning queue: counts and rows."), identityJobsOp(reg))
	gat.Register(api, g, op("identify", http.MethodPost, "/api/identify", "Say what a document IS: generate a caption/summary/kind, or record a person's."), identifyOp(reg))
	gat.Register(api, g, op("withdraw", http.MethodPost, "/api/withdraw", "Rule a document out of the corpus, with grounds. Survives re-ingest."), withdrawOp(reg))
	gat.Register(api, g, op("restore", http.MethodPost, "/api/restore", "Return a withdrawn document to the corpus (does not re-index)."), restoreOp(reg))
	gat.Register(api, g, op("listWithdrawals", http.MethodGet, "/api/withdrawals", "Documents ruled out of the corpus, and why."), withdrawalsOp(reg))
	gat.Register(api, g, op("problems", http.MethodGet, "/api/problems", "What is wrong with an index: unsearchable documents, failed jobs, degraded reads."), problemsOp(reg))
	gat.Register(api, g, op("materializeSlices", http.MethodPost, "/api/slices/materialize", "Build child documents for declared slices."), materializeSlicesOp(reg))
	gat.Register(api, g, op("listBranches", http.MethodGet, "/api/branches", "List branches: lineage, age, last-access, local doc count."), listBranchesOp(reg))
	gat.Register(api, g, op("forkBranch", http.MethodPost, "/api/branches", "Fork a branch off a parent index (copy-on-write overlay)."), forkBranchOp(reg))
	gat.Register(api, g, op("deleteBranch", http.MethodDelete, "/api/branches", "Delete a branch (its storage); parent untouched."), deleteBranchOp(reg))
	if watch != nil {
		gat.Register(api, g, op("listWatches", http.MethodGet, "/api/watch", "List project homes watched for auto re-ingest."), listWatchesOp(watch))
		gat.Register(api, g, op("addWatch", http.MethodPost, "/api/watch", "Register a project home for auto re-ingest on change."), addWatchOp(watch))
		gat.Register(api, g, op("removeWatch", http.MethodDelete, "/api/watch", "Unregister a watched project home."), removeWatchOp(watch))
	}
	if pool != nil {
		gat.Register(api, g, op("poolStats", http.MethodGet, "/api/pool", "Shared document-pool size (entries + files)."), poolStatsOp(pool))
		gat.Register(api, g, op("poolGC", http.MethodPost, "/api/pool/gc", "Evict pooled docs to a budget (max_bytes / max_entries / max_age_hours), oldest-accessed first."), poolGCOp(pool, defGC))
	}

	gat.Register(api, g, op("docDetail", http.MethodGet, "/api/doc-detail", "Everything known about one document: pages, transcript, where else it is seen, and how far its review has got."), docDetailOp(reg))
	gat.Register(api, g, op("attestWriteReadings", http.MethodPost, "/api/attest/readings", "Write readings across an index so its documents can be reviewed."), attestWriteReadingsOp(reg))

	// The review workbench, per index. Registered BEFORE RegisterHuma so its
	// operations land in the same OpenAPI document as everything else — which is
	// the property attest's Register was written for.
	if err := mountAttest(router, api, reg); err != nil {
		return nil, err
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
		// No caching. The page is embedded in the binary, so a copy held by a
		// browser is a page from a PREVIOUS BUILD — which presents as a fix that
		// did not take, and sends somebody debugging server code that is already
		// correct. attest's own UI has said this for as long as it has existed;
		// the daemon's had not.
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
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

// healthOut answers liveness, and identifies the build answering it.
//
// The build fields ride along on the probe every client already makes before it
// routes anything, so detecting a client/daemon mismatch costs no extra round
// trip. They are additive and optional: an older client ignores them, and a
// newer client treats their absence as "unknown" and stays quiet rather than
// guessing which side is older.
type healthOut struct {
	Body struct {
		Status string `json:"status"`
		// Revision is the daemon's vcs.revision, "" if the binary is unstamped.
		Revision string `json:"revision,omitempty"`
		// BuildTime is the daemon's vcs.time (RFC3339), the field that orders
		// two builds. "" if unstamped.
		BuildTime string `json:"build_time,omitempty"`
		// Modified reports a daemon built from a tree with uncommitted edits,
		// whose commit time therefore understates what it contains.
		Modified bool `json:"modified,omitempty"`
		// ExeHash is the sha256 of the running daemon binary — the only field
		// that can prove a client and this daemon are the SAME build when both
		// were built from dirty trees, which is the ordinary state during
		// development and the case commit metadata gets wrong.
		ExeHash string `json:"exe_hash,omitempty"`
	}
}

func health(_ context.Context, _ *struct{}) (*healthOut, error) {
	out := &healthOut{}
	out.Body.Status = "ok"
	out.Body.Revision = thisBuild.Revision
	if !thisBuild.Time.IsZero() {
		out.Body.BuildTime = thisBuild.Time.UTC().Format(time.RFC3339)
	}
	out.Body.Modified = thisBuild.Modified
	// Computed at most once per daemon, on the first probe that asks.
	out.Body.ExeHash = exeHash()
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
	// Origin marks a hit on the document's GENERATED caption/summary rather than
	// on the document (identity.go): findable by it, not quotable from it.
	Origin string `json:"origin,omitempty"`
}
type searchIn struct {
	Query string `query:"q"`
	Index string `query:"index"`
	Mode  string `query:"mode"`
	Path  string `query:"path"`
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
			hits, err := searchByMode(st, in.Query, in.Mode, in.Path, limit*2)
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
				Score: h.Score, Snippet: clip(oneLine(h.Text), 300), Origin: h.Origin,
			})
		}
		return out, nil
	}
}

type figureRow struct {
	Index       string  `json:"index"`
	MediaID     int64   `json:"media_id"`
	Path        string  `json:"path"`
	Title       string  `json:"title"`
	Page        int     `json:"page"`
	Description string  `json:"description"`
	ImagePath   string  `json:"image_path"`
	FragmentID  int64   `json:"fragment_id"`
	Score       float64 `json:"score"`
}
type searchFiguresIn struct {
	Query string `query:"q"`
	Index string `query:"index"`
	Path  string `query:"path"`
	Limit int    `query:"n"`
}
type searchFiguresOut struct {
	Body struct {
		Figures []figureRow `json:"figures"`
	}
}

func searchFiguresOp(reg *raglit.Registry, defLimit int) func(context.Context, *searchFiguresIn) (*searchFiguresOut, error) {
	return func(ctx context.Context, in *searchFiguresIn) (*searchFiguresOut, error) {
		if in.Query == "" {
			return nil, huma.Error400BadRequest("q is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = defLimit
		}
		out := &searchFiguresOut{}
		out.Body.Figures = []figureRow{}
		for _, name := range selectIndexes(reg, in.Index) {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			figs, err := st.SearchFiguresPath(ctx, in.Query, in.Path, limit)
			if err != nil {
				continue // no embedder on this index → skip
			}
			for _, f := range figs {
				title := f.Title
				if title == "" {
					title = f.Path
				}
				out.Body.Figures = append(out.Body.Figures, figureRow{
					Index: name, MediaID: f.MediaID, Path: f.Path, Title: title, Page: f.Page,
					Description: f.Description, ImagePath: f.ImagePath, FragmentID: f.FragmentID, Score: f.Score,
				})
			}
		}
		sort.SliceStable(out.Body.Figures, func(i, j int) bool { return out.Body.Figures[i].Score > out.Body.Figures[j].Score })
		if len(out.Body.Figures) > limit {
			out.Body.Figures = out.Body.Figures[:limit]
		}
		return out, nil
	}
}

type ingestIn struct {
	// Index as a QUERY parameter, matching every sibling endpoint.
	//
	// It was body-only, and that inconsistency is a silent one: a caller who
	// spells it the way /status, /search, /api/documents and /api/reread all
	// take it gets no error, no warning, and their work queued into the DEFAULT
	// index. Which is exactly what happened to this daemon's own UI.
	Index string `query:"index"`
	Body  struct {
		Targets []string `json:"targets"`
		Index   string   `json:"index,omitempty"`
		Title   string   `json:"title,omitempty"`
		// Fresh re-reads each target even if nothing changed, skipping both the
		// unchanged-bytes fast path and the cross-index pool. Additive and
		// optional: an older client omits it and gets the cached behaviour.
		Fresh bool `json:"fresh,omitempty"`
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
		// The body still wins when both are given, so existing callers are
		// unaffected; the query parameter is the fallback nobody had.
		idx := in.Body.Index
		if idx == "" {
			idx = in.Index
		}
		st, err := reg.Get(idx)
		if err != nil {
			return nil, huma.Error500InternalServerError("open index", err)
		}
		ids := make([]int64, 0, len(in.Body.Targets))
		for _, t := range in.Body.Targets {
			id, err := st.EnqueueFresh(t, in.Body.Title, in.Body.Fresh)
			if err != nil {
				return nil, huma.Error500InternalServerError("enqueue", err)
			}
			ids = append(ids, id)
		}
		out := &ingestOut{}
		out.Body.Queued = len(ids)
		out.Body.JobIDs = ids
		out.Body.Index = defaultIndexName(idx)
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
	FragMode  string `json:"frag_mode"`
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
					Fragments: d.Fragments, Pages: d.Pages, Vision: d.Vision, FragMode: d.FragMode,
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
		MaxBytes    int64   `json:"max_bytes,omitempty"`
		MaxEntries  int     `json:"max_entries,omitempty"`
		MaxAgeHours float64 `json:"max_age_hours,omitempty"`
	}
}
type poolGCOut struct {
	Body struct {
		Evicted int `json:"evicted"`
	}
}

// poolGCOp runs pool eviction, defaulting to the daemon's configured budget when
// the request omits a limit.
func poolGCOp(pool *raglit.Pool, def raglit.GCPolicy) func(context.Context, *poolGCIn) (*poolGCOut, error) {
	return func(_ context.Context, in *poolGCIn) (*poolGCOut, error) {
		pol := def
		if in.Body.MaxBytes > 0 {
			pol.MaxBytes = in.Body.MaxBytes
		}
		if in.Body.MaxEntries > 0 {
			pol.MaxEntries = in.Body.MaxEntries
		}
		if in.Body.MaxAgeHours > 0 {
			pol.MaxAgeUnused = time.Duration(in.Body.MaxAgeHours * float64(time.Hour))
		}
		n, err := pool.GC(pol)
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

type listWatchesOut struct {
	Body struct {
		Watches []watchInfo `json:"watches"`
	}
}

func listWatchesOp(w *watcher) func(context.Context, *struct{}) (*listWatchesOut, error) {
	return func(_ context.Context, _ *struct{}) (*listWatchesOut, error) {
		out := &listWatchesOut{}
		out.Body.Watches = w.List()
		if out.Body.Watches == nil {
			out.Body.Watches = []watchInfo{}
		}
		return out, nil
	}
}

type addWatchIn struct {
	Body struct {
		Home string `json:"home"`
	}
}

func addWatchOp(w *watcher) func(context.Context, *addWatchIn) (*okOut, error) {
	return func(_ context.Context, in *addWatchIn) (*okOut, error) {
		if in.Body.Home == "" {
			return nil, huma.Error400BadRequest("home is required")
		}
		if err := w.Add(in.Body.Home); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

type removeWatchIn struct {
	Home string `query:"home"`
}

func removeWatchOp(w *watcher) func(context.Context, *removeWatchIn) (*okOut, error) {
	return func(_ context.Context, in *removeWatchIn) (*okOut, error) {
		if err := w.Remove(in.Home); err != nil {
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
