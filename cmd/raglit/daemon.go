package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iodesystems/raglit"
)

// Daemon mode.
//
// `raglit daemon` is a long-running `serve` over HTTP: it owns the registry and
// runs the background ingest workers, and other invocations (ingest/search/
// status with --daemon or $RAGLIT_DAEMON) call INTO it instead of opening the
// SQLite files directly. That keeps one writer per index (no CLI contention with
// the workers) and lets ingest run in the background of a big job.
//
// Local-filesystem note: the daemon reads the paths it's given from ITS OWN
// filesystem (the client expands folders to absolute paths before sending), so
// local-file ingest works when client and daemon share a filesystem; http(s)://
// targets work from anywhere. Remote file UPLOAD is a later step. No auth yet —
// bind to localhost (the default) unless you add a proxy.

const defaultDaemonAddr = "127.0.0.1:7420"

// runReview is `daemon` framed as the review UI — same server (the UI is served
// at / and its control plane under /api), with a banner pointing at the page.
func runReview(args []string) error {
	fmt.Fprintln(os.Stderr, "raglit review — status, job control, and OCR review UI")
	return runDaemon(args)
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	_, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	addr := fs.String("addr", defaultDaemonAddr, "listen address")
	defLimit := fs.Int("n", 8, "default search results")
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runIndexWorkers(ctx, reg, lf, homeOf())

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/indexes", func(w http.ResponseWriter, _ *http.Request) {
		type idx struct {
			Name      string `json:"name"`
			Documents int    `json:"documents"`
			Fragments int    `json:"fragments"`
		}
		out := struct {
			Indexes []idx `json:"indexes"`
		}{Indexes: []idx{}}
		for _, name := range reg.Names() {
			if st, err := reg.Get(name); err == nil {
				s, _ := st.IndexStatus()
				out.Indexes = append(out.Indexes, idx{name, s.Documents, s.Fragments})
			}
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Targets []string `json:"targets"`
			Index   string   `json:"index"`
			Title   string   `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st, err := reg.Get(req.Index)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ids := make([]int64, 0, len(req.Targets))
		for _, t := range req.Targets {
			id, err := st.Enqueue(t, req.Title)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			ids = append(ids, id)
		}
		writeJSON(w, map[string]any{"queued": len(ids), "job_ids": ids})
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, "q is required", http.StatusBadRequest)
			return
		}
		limit := queryInt(r, "n", *defLimit)
		mode := r.URL.Query().Get("mode")
		lists := map[string][]raglit.Hit{}
		for _, name := range selectIndexes(reg, r.URL.Query().Get("index")) {
			st, err := reg.Get(name)
			if err != nil {
				continue
			}
			hits, err := searchByMode(st, q, mode, limit*2)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			lists[name] = hits
		}
		writeJSON(w, taggedHits(rrfMerge(lists, limit)))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, aggregateStatus(reg, selectIndexes(reg, r.URL.Query().Get("index"))))
	})

	// ── review UI + control plane ──────────────────────────────────────
	registerReviewRoutes(mux, reg, lf, homeOf())

	fmt.Fprintf(os.Stderr, "raglit daemon on http://%s (home %s)\n", *addr, homeOf())
	fmt.Fprintf(os.Stderr, "  review UI: http://%s/\n", *addr)
	return (&http.Server{Addr: *addr, Handler: mux}).ListenAndServe()
}

// registerReviewRoutes wires the review UI (served at /) and its JSON control
// plane: job listing + retry/cancel, the document list, per-document OCR review,
// page-image serving, and on-demand cascade re-OCR of a saved page image. All
// review endpoints are per-index (default "default").
func registerReviewRoutes(mux *http.ServeMux, reg *raglit.Registry, lf *llmFlags, home raglit.Home) {
	// getStore resolves the ?index (default "default"), 404-ing an unknown index.
	getStore := func(w http.ResponseWriter, r *http.Request) (*raglit.Store, bool) {
		st, err := reg.Get(r.URL.Query().Get("index"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return nil, false
		}
		return st, true
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(reviewHTML)
	})

	mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		st, ok := getStore(w, r)
		if !ok {
			return
		}
		jobs, err := st.Jobs(r.URL.Query().Get("state"), queryInt(r, "limit", 100))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if jobs == nil {
			jobs = []raglit.JobInfo{}
		}
		// Fold in ETA for pending/running items from the status snapshot.
		st2, _ := st.IndexStatus()
		eta := map[int64]float64{}
		for _, it := range st2.Items {
			eta[it.ID] = it.ETASeconds
		}
		type jobOut struct {
			raglit.JobInfo
			ETASeconds float64           `json:"eta_seconds"`
			Stages     []raglit.JobStage `json:"stages"`
		}
		out := make([]jobOut, len(jobs))
		for i, j := range jobs {
			stages, _ := st.JobStages(j.ID)
			if stages == nil {
				stages = []raglit.JobStage{}
			}
			out[i] = jobOut{JobInfo: j, ETASeconds: eta[j.ID], Stages: stages}
		}
		writeJSON(w, map[string]any{"jobs": out})
	})

	jobAction := func(action func(st *raglit.Store, id int64) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			st, ok := getStore(w, r)
			if !ok {
				return
			}
			var req struct {
				ID int64 `json:"id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := action(st, req.ID); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "id": req.ID})
		}
	}
	mux.HandleFunc("/api/jobs/retry", jobAction((*raglit.Store).RetryJob))
	mux.HandleFunc("/api/jobs/cancel", jobAction((*raglit.Store).CancelJob))

	mux.HandleFunc("/api/documents", func(w http.ResponseWriter, r *http.Request) {
		st, ok := getStore(w, r)
		if !ok {
			return
		}
		docs, err := st.Documents()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if docs == nil {
			docs = []raglit.DocSummary{}
		}
		writeJSON(w, map[string]any{"documents": docs})
	})

	mux.HandleFunc("/api/doc", func(w http.ResponseWriter, r *http.Request) {
		st, ok := getStore(w, r)
		if !ok {
			return
		}
		path := r.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		title, pages, err := st.DocReview(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pages == nil {
			pages = []raglit.PageReview{}
		}
		writeJSON(w, map[string]any{"path": path, "title": title, "pages": pages})
	})

	mux.HandleFunc("/api/page-image", func(w http.ResponseWriter, r *http.Request) {
		st, ok := getStore(w, r)
		if !ok {
			return
		}
		img, err := st.PageImagePath(r.URL.Query().Get("path"), queryInt(r, "page", 0))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Bound serving to the home's pages/ dir (no path traversal via the DB).
		root := st.PagesRoot()
		if img == "" || root == "" || !isUnder(img, root) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, img)
	})

	// On-demand re-OCR: rerun the cheap→gate→VLM cascade against a saved page
	// image and return {engine,text} — surfaces the escalation decision that
	// ingest (which OCRs+segments in one VLM call) doesn't record per page.
	mux.HandleFunc("/api/reocr", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		st, ok := getStore(w, r)
		if !ok {
			return
		}
		var req struct {
			Path string `json:"path"`
			Page int    `json:"page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		img, err := st.PageImagePath(req.Path, req.Page)
		if err != nil || img == "" || !isUnder(img, st.PagesRoot()) {
			http.Error(w, "no saved page image for that page", http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(img)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ocr := buildToolOCR(lf, home)
		text, engine, err := ocr.PageWithEngine(r.Context(), raglit.PageImage{
			Page: req.Page, Mime: mimeForImage(img), Data: data,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"page": req.Page, "engine": engine, "text": text})
	})
}

// isUnder reports whether path is within dir (both cleaned, absolute-ish). Used
// to bound page-image serving to the home's pages/ directory.
func isUnder(path, dir string) bool {
	if dir == "" {
		return false
	}
	ap, err1 := filepath.Abs(path)
	ad, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(ad, ap)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// searchByMode dispatches bm25 (default) / vec / hybrid.
func searchByMode(st *raglit.Store, q, mode string, limit int) ([]raglit.Hit, error) {
	switch mode {
	case "vec":
		return st.VecSearch(context.Background(), q, limit)
	case "hybrid":
		return st.HybridSearch(context.Background(), q, limit)
	default:
		return st.Search(q, limit)
	}
}

// aggregateStatus sums status across the named indexes.
func aggregateStatus(reg *raglit.Registry, names []string) raglit.Status {
	var agg raglit.Status
	for _, name := range names {
		st, err := reg.Get(name)
		if err != nil {
			continue
		}
		s, err := st.IndexStatus()
		if err != nil {
			continue
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
	return agg
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ── client side ────────────────────────────────────────────────────

// addDaemonFlag registers --daemon (default $RAGLIT_DAEMON). When set, a command
// routes to a running daemon instead of opening the index files directly.
func addDaemonFlag(fs *flag.FlagSet) *string {
	return fs.String("daemon", os.Getenv("RAGLIT_DAEMON"),
		"route to a running daemon (e.g. http://host:7420); default $RAGLIT_DAEMON")
}

func daemonIngest(base string, targets []string, index, title string) error {
	body, _ := json.Marshal(map[string]any{"targets": targets, "index": index, "title": title})
	resp, err := http.Post(strings.TrimRight(base, "/")+"/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon ingest: %s", string(b))
	}
	fmt.Printf("%s\n", b)
	return nil
}

func daemonGet(base, path string, q url.Values) ([]byte, error) {
	u := strings.TrimRight(base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon %s: %s", path, string(b))
	}
	return b, nil
}

// daemonSearchPrint queries the daemon and prints ranked hits.
func daemonSearchPrint(base, query, index, mode string, limit int) error {
	q := url.Values{"q": {query}, "mode": {mode}, "n": {strconv.Itoa(limit)}}
	if index != "" {
		q.Set("index", index)
	}
	b, err := daemonGet(base, "/search", q)
	if err != nil {
		return err
	}
	var resp struct {
		Hits []struct {
			Index   string  `json:"index"`
			DocID   string  `json:"doc_id"`
			Page    int     `json:"page"`
			Score   float64 `json:"score"`
			Snippet string  `json:"snippet"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return err
	}
	if len(resp.Hits) == 0 {
		fmt.Println("(no matches)")
		return nil
	}
	for i, h := range resp.Hits {
		loc := h.DocID
		if h.Page > 0 {
			loc = fmt.Sprintf("%s p%d", h.DocID, h.Page)
		}
		fmt.Printf("%d. [%.3f] (%s) %s\n   %s\n", i+1, h.Score, h.Index, loc, clip(oneLine(h.Snippet), 160))
	}
	return nil
}

// daemonStatusPrint fetches + renders the daemon's status.
func daemonStatusPrint(base, index string) error {
	q := url.Values{}
	if index != "" {
		q.Set("index", index)
	}
	b, err := daemonGet(base, "/status", q)
	if err != nil {
		return err
	}
	var st raglit.Status
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	renderStatus(st)
	return nil
}
