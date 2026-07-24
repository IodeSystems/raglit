package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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

// defaultDaemonURL is the shared per-user daemon every client ensures by default.
const defaultDaemonURL = "http://" + defaultDaemonAddr

// ensureDaemon returns a reachable shared-daemon base URL, auto-starting a LOCAL
// daemon if none is running — so every session/CLI is a thin client to ONE daemon
// (single writer + single worker pool + single LLM caller), never N processes
// opening the index in-process. Target: --daemon > $RAGLIT_DAEMON > config
// daemon_url, else the default localhost daemon. A REMOTE target that's down is an
// error (can't start it). Race-safe: concurrent starters collide on the port bind;
// the loser exits and everyone connects to the winner.
func ensureDaemon(flagVal string, homeOf func() raglit.Home) (string, error) {
	base := resolveDaemon(flagVal, homeOf)
	if base == "" {
		// No explicit target: discover a running daemon from its state file, so one
		// on a non-default port is found instead of a duplicate spawned on 7420. Only
		// trust it if the pid is alive AND it answers health (guards a stale file).
		if st, ok := readDaemonState(raglit.DefaultRoot()); ok && pidAlive(st.PID) {
			if u := "http://" + st.Addr; daemonHealthy(u) {
				return u, nil
			}
		}
		base = defaultDaemonURL
	}
	if daemonHealthy(base) {
		return base, nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("bad daemon url %q: %w", base, err)
	}
	if !isLoopback(u.Hostname()) {
		return "", fmt.Errorf("daemon at %s is unreachable and not local — start it there", base)
	}
	if err := startDaemonDetached(u.Host); err != nil {
		return "", fmt.Errorf("auto-start daemon: %w", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if daemonHealthy(base) {
			return base, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return "", fmt.Errorf("auto-started daemon did not come up at %s (see %s)", base, filepath.Join(raglit.DefaultRoot(), "daemon.log"))
}

// daemonHealthy reports whether a daemon answers /api/health at base.
func daemonHealthy(base string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func isLoopback(host string) bool {
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// startDaemonDetached launches `raglit daemon --addr <addr>` as a detached process
// (own session, output appended to <root>/daemon.log) so the shared daemon
// outlives the client that started it.
func startDaemonDetached(addr string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root := raglit.DefaultRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "daemon", "--addr", addr)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = logf, logf, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from our session
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release() // keep it running after we exit
}

// addClientFlags registers --daemon, --embedded, and --project, returning a
// resolver that yields (daemonURL, projectNamespace). In daemon mode it
// auto-starts the shared daemon if needed and REQUIRES a project name (flag or
// config "project") — that namespaces this client's indexes so projects don't
// collide on the shared daemon. --embedded (or a raw --db) opts out: returns
// ("", "") to open the index in-process, no project needed.
func addClientFlags(fs *flag.FlagSet) (resolve func(homeOf func() raglit.Home, dbSet bool) (durl, ns string, err error)) {
	daemon := addDaemonFlag(fs)
	embedded := fs.Bool("embedded", false, "bypass the shared daemon; open the index in-process (no project needed)")
	project := fs.String("project", "", `project name — namespaces this client's indexes on the shared daemon (required; default: config "project")`)
	return func(homeOf func() raglit.Home, dbSet bool) (string, string, error) {
		if *embedded || dbSet {
			return "", "", nil
		}
		durl, err := ensureDaemon(*daemon, homeOf)
		if err != nil {
			return "", "", err
		}
		ns, err := resolveProject(*project, homeOf)
		if err != nil {
			return "", "", err
		}
		return durl, ns, nil
	}
}

// resolveProject picks the project namespace: an explicit --project wins, else the
// home config's "project". Required in daemon mode — an empty result is an error
// (the shared daemon namespaces by project, so a client without one is refused).
func resolveProject(flagVal string, homeOf func() raglit.Home) (string, error) {
	raw := strings.TrimSpace(flagVal)
	if raw == "" {
		if cfg, _, _ := raglit.LoadConfig(homeOf()); cfg.Project != "" {
			raw = cfg.Project
		}
	}
	if raw == "" {
		return "", fmt.Errorf("no project name — set \"project\" in %s (run `raglit init`) or pass --project. "+
			"The shared daemon namespaces every project's indexes by name so they don't collide; a client without one is refused. "+
			"(Use --embedded for a single-session, in-process index with no project.)", homeOf().ConfigPath())
	}
	return raglit.NormalizeIndexName(raw), nil
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

// daemonDelete performs a daemon DELETE and returns the body, erroring on non-200.
func daemonDelete(base, path string, q url.Values) ([]byte, error) {
	u := strings.TrimRight(base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
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

// daemonSearchPrint queries the daemon and prints ranked hits. ns is the project
// namespace, stripped from each hit's index tag for display.
func daemonSearchPrint(base, query, index, mode string, limit int, ns string) error {
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
		fmt.Printf("%d. [%.3f] (%s) %s\n   %s\n", i+1, h.Score, nsStrip(ns, h.Index), loc, clip(oneLine(h.Snippet), 160))
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
