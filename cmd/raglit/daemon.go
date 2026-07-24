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

// runDaemon runs the gat multi-protocol daemon (httpd.go). The legacy stdlib
// net/http server + review routes were retired once httpd reached parity — the
// gat server serves the same paths (REST + review UI + OpenAPI + GraphQL) plus
// GraphQL/gRPC and branch endpoints.
func runDaemon(args []string) error { return runHttpd(args) }

// runReview is the same daemon, framed as the review UI with a banner.
func runReview(args []string) error {
	fmt.Fprintln(os.Stderr, "raglit review — status, job control, and OCR review UI")
	return runHttpd(args)
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

// daemonPostJSON POSTs body as JSON and returns the response body, erroring on a
// non-200 (with the daemon's error body). Used by the MCP client proxies.
func daemonPostJSON(base, path string, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(strings.TrimRight(base, "/")+path, "application/json", bytes.NewReader(buf))
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
