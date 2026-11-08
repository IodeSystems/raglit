package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// expandIngestTargets turns ingest args into a flat list of ingestable targets:
// a URL (file://, http(s)://) passes through; a local directory is walked for
// every file raglit can read (ClassifyDoc decides); a local file becomes its
// absolute path and is queued whatever it is. So `ingest ./repo` queues every
// readable file under repo.
func expandIngestTargets(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		if strings.Contains(a, "://") { // a URL
			out = append(out, a)
			continue
		}
		fi, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			err = filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				// ClassifyDoc is the single authority on what raglit can read.
				// This walk used to test isText||isPDF, which silently skipped
				// every office format and every image the extractor handles — a
				// directory of .docx or scanned .tif enqueued nothing and looked
				// like it had been covered. A format added to the extractor must
				// become discoverable in the same change, not the next one.
				// Skipped silently in a WALK: a directory ingest should not
				// report an error per generated file. Naming one explicitly still
				// gets the refusal, from Enqueue.
				if !d.IsDir() && !raglit.IsGeneratedSidecar(p) &&
					raglit.ClassifyDoc(p, "") != raglit.KindUnknown {
					abs, _ := filepath.Abs(p)
					out = append(out, abs)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			abs, _ := filepath.Abs(a)
			out = append(out, abs)
		}
	}
	return out, nil
}

// buildWorker fills raglit.Models from the home's config and the flags, and
// builds the index's worker from it (raglit.NewWorker).
func buildWorker(store *raglit.Store, lf *llmFlags, home raglit.Home, pool *raglit.Pool) *raglit.Worker {
	return raglit.NewWorker(store, lf.models(home), pool)
}

// runIngest queues targets on the daemon. With --wait it also waits until the
// index's queue is empty, then prints its status.
func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	homeOf, index := addHomeFlags(fs)
	client := addClientFlags(fs) // --daemon + --project
	title := fs.String("title", "", "document title (single-URL convenience)")
	wait := fs.Bool("wait", false, "wait until the daemon has worked the queue, then print status")
	fs.BoolVar(wait, "now", false, "old name of --wait")
	_ = fs.MarkHidden("now")
	fresh := fs.Bool("fresh", false, "re-read even if unchanged: skip the unchanged-bytes check AND the cross-index pool")
	name := fs.String("name", "", "with --text-file: the document's name in the index (slash-separated)")
	textFile := fs.String("text-file", "", "ingest this file's text as document --name, sent to the daemon (- reads stdin)")
	identityFile := fs.String("identity", "", `with --text-file: a JSON file {"name","summary","kind"} recorded as the document's caption (gen_source "supplied"; no model call)`)
	fs.Parse(args)
	dURL, ns, err := client(homeOf)
	if err != nil {
		return err
	}
	idx, err := nsWriteIndex(ns, projectShared(homeOf), resolveIndexName(*index, homeOf))
	if err != nil {
		return err
	}
	if *textFile != "" {
		if err := ingestText(dURL, idx, *name, *title, *textFile, *identityFile); err != nil {
			return err
		}
	} else {
		if *identityFile != "" || *name != "" {
			return fmt.Errorf("ingest: --name and --identity go with --text-file")
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("ingest: nothing given (a folder, file, file://<path>, or http(s)://..., or --text-file)")
		}
		targets, err := expandIngestTargets(fs.Args())
		if err != nil {
			return err
		}
		if err := daemonIngest(dURL, targets, idx, *title, *fresh, projectDirOf(homeOf())); err != nil {
			return err
		}
	}
	if *wait {
		return waitForQueue(dURL, idx)
	}
	return nil
}

// ingestText sends text as document name through the upload route, with the
// caption in identityFile when given. How a session's fold or recap goes into
// its branch: `raglit ingest --index sessions@<id> --name fold-3 --text-file -
// --identity fold-3.json`.
func ingestText(base, index, name, title, textFile, identityFile string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("ingest --text-file: --name is required")
	}
	var text []byte
	var err error
	if textFile == "-" {
		text, err = io.ReadAll(os.Stdin)
	} else {
		text, err = os.ReadFile(textFile)
	}
	if err != nil {
		return err
	}
	body := map[string]any{"name": name, "text": string(text), "title": title}
	if identityFile != "" {
		b, err := os.ReadFile(identityFile)
		if err != nil {
			return err
		}
		var id struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
			Kind    string `json:"kind"`
		}
		if err := json.Unmarshal(b, &id); err != nil {
			return fmt.Errorf("ingest --identity %s: %w", identityFile, err)
		}
		body["identity"] = id
	}
	b, err := daemonPostJSON(base, "/api/upload?index="+url.QueryEscape(index), body)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", b)
	return nil
}

// waitForQueue polls the daemon until index has nothing pending or running,
// then prints its status. The daemon does the work; this only watches.
func waitForQueue(base, index string) error {
	for {
		b, err := daemonGet(base, "/status", url.Values{"index": {index}})
		if err != nil {
			return err
		}
		var st raglit.Status
		if err := json.Unmarshal(b, &st); err != nil {
			return err
		}
		if st.Pending == 0 && st.Running == 0 {
			renderStatus(st)
			return nil
		}
		time.Sleep(time.Second)
	}
}

// runWork waits until the daemon has worked the index's queue, then prints
// its status. It used to drain the queue in-process; the daemon is the only
// worker now (USER 2026-10-30: always a daemon).
func runWork(args []string) error {
	fs := flag.NewFlagSet("work", flag.ExitOnError)
	resolve := addTargetFlags(fs)
	fs.Parse(args)
	t, err := resolve()
	if err != nil {
		return err
	}
	return waitForQueue(t.base, t.index)
}

// runStatus prints the index + queue status.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	homeOf, index := addHomeFlags(fs)
	client := addClientFlags(fs) // --daemon + --project
	fs.Parse(args)
	dURL, ns, err := client(homeOf)
	if err != nil {
		// No project to resolve. Show what EXISTS instead of only naming a file to
		// edit — this is the command people run to find out where they are.
		return printIndexDirectory(err)
	}
	return daemonStatusPrint(dURL, nsReadSelector(ns, projectShared(homeOf), *index))
}

// renderStatus prints a Status value (shared by the local + daemon paths).
func renderStatus(st raglit.Status) {
	fmt.Printf("index: %d document(s), %d fragment(s)\n", st.Documents, st.Fragments)
	fmt.Printf("jobs:  done=%d running=%d pending=%d failed=%d", st.Done, st.Running, st.Pending, st.Failed)
	if st.RatePerMin > 0 {
		fmt.Printf("  (%.1f/min)", st.RatePerMin)
	}
	fmt.Println()
	if iq := st.Identity; !iq.Empty() || iq.Failed > 0 {
		fmt.Printf("names: %d pending, %d running, %d captioned, %d skipped, %d failed  (identify)\n",
			iq.Pending, iq.Running, iq.Done, iq.Skipped, iq.Failed)
	}
	if st.Prunable > 0 {
		fmt.Printf("prune: %d document(s) whose file is gone — out of search; remove with `raglit prune`\n", st.Prunable)
	}
	for _, it := range st.Items {
		eta := "eta n/a"
		if it.ETASeconds > 0 {
			eta = fmt.Sprintf("eta ~%.0fs", it.ETASeconds)
		}
		fmt.Printf("  %-8s #%d %s  (%s)\n", it.State, it.ID, it.URL, eta)
	}
}

// printIndexDirectory is what `raglit status` shows when it has no project to
// resolve — i.e. run anywhere outside a configured project.
//
// Refusing with "no project name — set project in ..." is correct and useless:
// it names a file to edit without saying what exists, so the indexes the daemon
// is holding, and their paths, are undiscoverable from the command whose whole
// job is to report state. A tool that knows the answer should say it.
func printIndexDirectory(reason error) error {
	root := raglit.DefaultRoot()
	fmt.Printf("not in a project — no index selected.\n")
	if reason != nil {
		fmt.Printf("  (%s)\n", firstSentence(reason.Error()))
	}
	fmt.Printf("\nstorage root: %s\n", root)

	// daemon.json records where a daemon WAS; it survives a kill, so believing it
	// would report a dead daemon as running. Probe.
	live := "" // the recorded daemon, when it answers; never auto-started here
	if st, recorded := readDaemonState(root); !recorded {
		fmt.Println("daemon:       not running")
	} else if base := "http://" + st.Addr; daemonHealthy(base) {
		live = base
		trustDaemon(base)
		fmt.Printf("daemon:       %s  (pid %d, since %s)\n", base, st.PID, st.StartedAt)
	} else {
		fmt.Printf("daemon:       not running  (stale %s records pid %d at %s)\n",
			filepath.Base(daemonStatePath(root)), st.PID, st.Addr)
	}

	// The indexes themselves, with counts from the daemon. With it down, the
	// names from disk and "?" for the counts: no command opens an index file.
	counts := map[string]idxRow{}
	if live != "" {
		var l struct {
			Indexes []idxRow `json:"indexes"`
		}
		if b, err := daemonGet(live, "/indexes", nil); err == nil && json.Unmarshal(b, &l) == nil {
			for _, r := range l.Indexes {
				counts[r.Name] = r
			}
		}
	}
	dir := filepath.Join(root, "indexes")
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		fmt.Printf("\nno indexes under %s\n", dir)
		fmt.Println("`raglit init` in a project directory creates one.")
		return nil
	}
	fmt.Printf("\n%-34s %-9s %-11s %s\n", "INDEX", "DOCS", "FRAGMENTS", "PATH")
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		docs, frags := "?", "?" // "?" is a claim about our access, 0 about the index
		if r, ok := counts[name]; ok {
			docs, frags = fmt.Sprint(r.Documents), fmt.Sprint(r.Fragments)
		}
		fmt.Printf("%-34s %-9s %-11s %s\n", name, docs, frags, filepath.Join(dir, name, "index.sqlite"))
	}
	fmt.Println("\nAn index is named <project>__<index>. To act on one:")
	fmt.Println("  cd into the project (its .raglit/config.json names it), or")
	fmt.Println("  raglit status --project <project> [--index <index>]")
	return nil
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// runRetry requeues failed ingest jobs.
//
// The case this exists for: a transient outage — the vision model unloaded
// during a restart — failed 72 PDFs in one window with "pdf ingest needs a
// vision model". Nothing surfaced `RetryJob`, so recovering meant reading URLs
// out of SQLite by hand and feeding them back to `ingest`.
//
// A vanished path is SKIPPED, not fatal. Failed rows outlive renames, and
// `ingest` aborts on the first missing file — one stale path in a batch of 122
// killed the whole retry. Reporting the skips is the point: after a rename
// sweep, "11 gone" is the answer, not an error.
func runRetry(args []string) error {
	fs := flag.NewFlagSet("retry", flag.ExitOnError)
	resolve := addTargetFlags(fs)
	dryRun := fs.Bool("dry-run", false, "list what would be requeued, change nothing")
	state := fs.String("state", "error", "which state to requeue: error|done")
	limit := fs.Int("limit", 0, "cap how many are requeued (0 = all)")
	match := fs.String("match", "", "only jobs whose error contains this substring")
	fs.Parse(args)
	t, err := resolve()
	if err != nil {
		return err
	}
	// Which index is this? Printed because its absence once hid that status
	// and retry were reading different indexes.
	fmt.Printf("index: %s\n", t.local())

	var jl struct {
		Jobs []raglit.JobInfo `json:"jobs"`
	}
	if err := t.get("/api/jobs", url.Values{"state": {*state}, "limit": {"100000"}}, &jl); err != nil {
		return err
	}
	var requeued, missing, filtered int
	for _, j := range jl.Jobs {
		if *match != "" && !strings.Contains(j.Error, *match) {
			filtered++
			continue
		}
		if p := localPath(j.URL); p != "" {
			if _, err := os.Stat(p); err != nil {
				missing++
				fmt.Printf("  gone     %s\n", p)
				continue
			}
		}
		if *limit > 0 && requeued >= *limit {
			break
		}
		if *dryRun {
			fmt.Printf("  would    #%d %s\n", j.ID, j.URL)
			requeued++
			continue
		}
		if err := t.post("/api/jobs/retry", map[string]any{"id": j.ID}, nil); err != nil {
			fmt.Fprintf(os.Stderr, "  failed   #%d: %v\n", j.ID, err)
			continue
		}
		requeued++
	}

	verb := "requeued"
	if *dryRun {
		verb = "would requeue"
	}
	fmt.Printf("%s %d · %d gone (skipped)", verb, requeued, missing)
	if filtered > 0 {
		fmt.Printf(" · %d filtered out by --match", filtered)
	}
	fmt.Println()
	if missing > 0 {
		fmt.Println("  gone = the row outlived the file, usually a rename. Nothing to do.")
	}
	return nil
}

// localPath returns the filesystem path a job URL refers to, or "" when the job
// is not a local file (http(s) targets have nothing to stat).
func localPath(u string) string {
	switch {
	case strings.HasPrefix(u, "file://"):
		return strings.TrimPrefix(u, "file://")
	case strings.HasPrefix(u, "/"):
		return u
	}
	return ""
}
