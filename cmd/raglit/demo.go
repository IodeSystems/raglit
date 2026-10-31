package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

//go:embed demodata/*.md
var demoDocs embed.FS

// runDemo is a self-contained, offline tour: it starts a throwaway daemon on a
// temp root and a free loopback port, queues a small embedded corpus as file://
// URLs (lazy), prints the queue status as the daemon works it — so the pending
// count, rate and ETAs are visible — then searches. No LLM/config needed, and
// the user's own daemon is never touched. It goes through a daemon like every
// other command (USER 2026-10-30: always a daemon).
func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	keep := fs.Bool("keep", false, "keep the temp demo root instead of deleting it")
	fs.Parse(args)

	dir, err := os.MkdirTemp("", "raglit-demo-")
	if err != nil {
		return err
	}
	if *keep {
		fmt.Printf("demo root: %s (kept)\n", dir)
	} else {
		defer os.RemoveAll(dir)
	}

	// Materialize the embedded corpus so it can be ingested by file:// URL.
	corpus := filepath.Join(dir, "corpus")
	if err := os.MkdirAll(corpus, 0o755); err != nil {
		return err
	}
	entries, _ := demoDocs.ReadDir("demodata")
	var urls []string
	for _, e := range entries {
		b, _ := demoDocs.ReadFile("demodata/" + e.Name())
		p := filepath.Join(corpus, e.Name())
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		urls = append(urls, "file://"+p)
	}

	base, stop, err := startDemoDaemon(filepath.Join(dir, "root"))
	if err != nil {
		return err
	}
	defer stop()
	const index = "demo"
	fmt.Printf("raglit demo — %d docs into a throwaway daemon at %s\n\n", len(urls), base)

	// LAZY: queue everything at once (instant); the daemon's worker drains it.
	if err := daemonIngest(base, urls, index, "", false, ""); err != nil {
		return err
	}
	fmt.Print("\nworking the queue (status as it changes):\n\n")
	last := ""
	for {
		b, err := daemonGet(base, "/status", url.Values{"index": {index}})
		if err != nil {
			return err
		}
		var st raglit.Status
		if err := json.Unmarshal(b, &st); err != nil {
			return err
		}
		line := fmt.Sprintf("%d/%d/%d", st.Done, st.Running, st.Pending)
		if line != last {
			renderStatus(st)
			fmt.Println()
			last = line
		}
		if st.Pending == 0 && st.Running == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	for _, q := range []string{"how does token refresh work", "rolling back a bad release"} {
		fmt.Printf("search %q:\n", q)
		if err := daemonSearchPrint(base, q, index, "bm25", "", 2, ""); err != nil {
			return err
		}
		fmt.Println()
	}
	fmt.Println("that's the lazy-ingest + status + search loop. `raglit mcp` exposes")
	fmt.Println("the same as MCP tools: ingest, index_status, search.")
	return nil
}

// startDemoDaemon runs `raglit daemon` on root and a free loopback port, as a
// child that neither rebuilds itself nor hands off to a systemd unit, and
// waits for it to answer. stop ends it.
func startDemoDaemon(root string) (base string, stop func(), err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	addr := l.Addr().String()
	l.Close()
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	cmd := exec.Command(exe, "daemon", "--root", root, "--addr", addr)
	cmd.Env = append(os.Environ(), "RAGLIT_CHILD=1", "RAGLIT_NO_AUTOBUILD=1", "RAGLIT_ROOT="+root)
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	stop = func() { _ = cmd.Process.Signal(os.Interrupt); _, _ = cmd.Process.Wait() }
	base = "http://" + addr
	trustDaemon(base)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if daemonHealthy(base) {
			return base, stop, nil
		}
	}
	stop()
	return "", nil, fmt.Errorf("demo daemon did not come up at %s", base)
}
