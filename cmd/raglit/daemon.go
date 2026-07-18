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

	fmt.Fprintf(os.Stderr, "raglit daemon on http://%s (home %s)\n", *addr, homeOf())
	return (&http.Server{Addr: *addr, Handler: mux}).ListenAndServe()
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
