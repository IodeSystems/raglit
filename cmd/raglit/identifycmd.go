package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// `raglit identify` — what a document IS, for a corpus that is already indexed.
//
// Identity is generated at ingest (identity.go), but a corpus predating it holds
// hundreds of documents named by a scanner, and re-OCR'ing them to obtain a
// caption would be absurd: the text is already in the index. This reads it back
// and asks the model, one document at a time, resumable — every caption is
// committed as it is produced, so an interrupted run keeps what it did.
//
// It is also where a PERSON overrules the machine: --name/--summary/--kind
// record an identity attributed to them, and nothing regenerates it afterwards.
func runIdentify(args []string) error {
	fs := flag.NewFlagSet("identify", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	list := fs.Bool("list", false, "show what each document is (no model calls)")
	force := fs.Bool("force", false, "re-generate captions that already exist (a person's is never replaced)")
	dry := fs.Bool("dry-run", false, "name the documents that would be captioned")
	limit := fs.Int("limit", 0, "stop after this many documents (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable")
	name := fs.String("name", "", "record THIS name for the document (a person's ruling)")
	summary := fs.String("summary", "", "record this summary (with --name)")
	kind := fs.String("kind", "", "record this kind: "+strings.Join(raglit.IdentityKinds(), " | "))
	by := fs.String("by", defaultWithdrawBy(), "who is recording it (with --name)")
	wait := fs.Bool("wait", false, "follow the queue until it drains (the work continues either way)")
	tagsOnly := fs.Bool("tags-only", false, "ask for TAGS only, leaving existing captions alone (the backfill for a corpus captioned before tags existed)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()

	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	lf.resolve(homeOf())

	if *list {
		return printIdentities(st, targets, *asJSON)
	}

	// A write goes through the daemon on a daemon-routed project — the daemon is
	// the single writer for its indexes, and writing behind it here would also
	// write a DIFFERENT index from the one search reads.
	ns, _ := resolveProject("", homeOf)
	routed := ns != "" && !explicitStoreFlag(fs)

	// A person's ruling: one document, recorded, done.
	if strings.TrimSpace(*name) != "" || strings.TrimSpace(*summary) != "" || strings.TrimSpace(*kind) != "" {
		if len(targets) != 1 {
			return fmt.Errorf("identify: name exactly one document to record an identity for")
		}
		path, err := resolveOneDoc(st, targets[0])
		if err != nil {
			return err
		}
		want := raglit.DocIdentity{Name: *name, Summary: *summary, Kind: *kind}
		var got raglit.DocIdentity
		if routed {
			got, err = daemonIdentify(path, want, *by, false)
		} else {
			got, err = st.RecordIdentity(context.Background(), path, want, *by)
		}
		if err != nil {
			return err
		}
		printIdentity(path, got)
		return nil
	}

	paths, err := identifyTargets(st, targets, *force, *tagsOnly)
	if err != nil {
		return err
	}
	if *limit > 0 && len(paths) > *limit {
		paths = paths[:*limit]
	}
	if len(paths) == 0 {
		if *tagsOnly {
			fmt.Println("nothing to tag — every captioned document has tags")
		} else {
			fmt.Println("nothing to identify — every document has a name")
		}
		return nil
	}
	if *dry {
		for _, p := range paths {
			fmt.Println(p)
		}
		fmt.Printf("would identify %d document(s)\n", len(paths))
		return nil
	}

	// QUEUED, not looped. A caption is a model call, the endpoint runs two at a
	// time, and a corpus is hundreds — so the command's job is to record the work
	// durably and get out of the way. The rows outlive this process; the daemon's
	// identity worker drains them at the endpoint's real concurrency. --wait
	// follows along, and closing it stops the watching, not the work.
	ctx := context.Background()
	queued, err := enqueueIdentityWork(st, paths, *force, *tagsOnly, routed)
	if err != nil {
		return err
	}
	q := identityQueueNow(st, routed)
	fmt.Printf("queued %d document(s) — %d pending, %d running, %d captioned, %d skipped, %d failed\n",
		queued, q.Pending, q.Running, q.Done, q.Skipped, q.Failed)
	if !routed {
		// No daemon to drain them: this process is the worker. Same queue, same
		// rows, same resumability — an interrupted run leaves the rest pending.
		return drainIdentityLocally(ctx, st, lf, homeOf())
	}
	if !*wait {
		fmt.Println("the daemon is working them; `raglit identify --wait` follows, `--list` shows what is named")
		return nil
	}
	return waitForIdentityQueue(st)
}

// enqueueIdentityWork records the work: through the daemon when routed (it owns
// the index), directly otherwise.
func enqueueIdentityWork(st *raglit.Store, paths []string, force, tagsOnly, routed bool) (int, error) {
	if !routed {
		if tagsOnly {
			return st.EnqueueTagsFor(paths, force)
		}
		return st.EnqueueIdentityFor(paths, force)
	}
	n := 0
	for _, p := range paths {
		queued, err := daemonEnqueueIdentity(p, force, tagsOnly)
		if err != nil {
			return n, err
		}
		n += queued
	}
	return n, nil
}

// identityQueueNow reads the queue's counts, from whichever side owns them. The
// local read is the same rows the daemon writes, so it is accurate either way;
// this exists so a failure to reach the daemon degrades to zeroes rather than
// aborting a sweep that was already recorded.
func identityQueueNow(st *raglit.Store, routed bool) raglit.IdentityQueueStatus {
	if routed {
		if q, err := daemonIdentityQueue(); err == nil {
			return q
		}
	}
	q, _ := st.IdentityQueue()
	return q
}

// drainIdentityLocally works the queue in-process, for an embedded index with no
// daemon behind it. Progress prints as each caption lands, because here the
// person watching IS the worker.
func drainIdentityLocally(ctx context.Context, st *raglit.Store, lf *llmFlags, home raglit.Home) error {
	id := lf.identifier(home)
	if id == nil {
		return fmt.Errorf("identify: no identity model configured — run 'raglit init' or set identity_model")
	}
	st.SetIdentifier(id)
	cfg, _, _ := raglit.LoadConfig(home)
	w := &raglit.IdentityWorker{Store: st, Slots: cfg.IdentitySlots}
	done, failed := 0, 0
	w.OnDone = func(job raglit.IdentityJob, got raglit.DocIdentity, err error) {
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", job.Path, err)
			return
		}
		done++
		printIdentity(job.Path, got)
	}
	if _, err := w.Drain(ctx); err != nil {
		return err
	}
	fmt.Printf("identified %d document(s)", done)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()
	return nil
}

// waitForIdentityQueue follows the daemon's progress until the queue is empty.
// Interrupting it stops the watching, not the work.
func waitForIdentityQueue(st *raglit.Store) error {
	last := ""
	for {
		q, err := daemonIdentityQueue()
		if err != nil {
			q, _ = st.IdentityQueue()
		}
		line := fmt.Sprintf("%d pending, %d running, %d captioned, %d skipped, %d failed",
			q.Pending, q.Running, q.Done, q.Skipped, q.Failed)
		if line != last {
			fmt.Println(line)
			last = line
		}
		if q.Empty() {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
}

// identifyTargets is the work list: the named documents, or every document with
// no caption yet (with --force, every document).
func identifyTargets(st *raglit.Store, targets []string, force, tagsOnly bool) ([]string, error) {
	if len(targets) > 0 {
		docs, err := st.Documents()
		if err != nil {
			return nil, err
		}
		paths := matchIndexed(docs, targets)
		if len(paths) == 0 {
			return nil, fmt.Errorf("identify: nothing indexed under %s", strings.Join(targets, ", "))
		}
		return paths, nil
	}
	if force {
		docs, err := st.Documents()
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(docs))
		for _, d := range docs {
			out = append(out, d.Path)
		}
		return out, nil
	}
	if tagsOnly {
		return st.DocumentsMissingTags()
	}
	return st.DocumentsMissingIdentity()
}

// resolveOneDoc turns a filename or substring into the one indexed path it
// names, refusing an ambiguous reference rather than picking.
func resolveOneDoc(st *raglit.Store, ref string) (string, error) {
	cands, err := st.MatchDocuments(ref)
	if err != nil {
		return "", err
	}
	switch len(cands) {
	case 0:
		return "", fmt.Errorf("identify: no indexed document matches %q", ref)
	case 1:
		return cands[0].Path, nil
	default:
		var b strings.Builder
		for _, c := range cands {
			b.WriteString("\n  " + c.Path)
		}
		return "", fmt.Errorf("identify: %q matches %d documents:%s", ref, len(cands), b.String())
	}
}

// printIdentities shows what the corpus knows itself to be, and how much of it
// does not. The coverage line is the point: a caption nobody has is invisible in
// a list of the ones that exist.
func printIdentities(st *raglit.Store, targets []string, asJSON bool) error {
	all, err := st.Identities()
	if err != nil {
		return err
	}
	rows := all
	if len(targets) > 0 {
		docs, err := st.Documents()
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, p := range matchIndexed(docs, targets) {
			want[p] = true
		}
		rows = nil
		for _, r := range all {
			if want[r.Path] {
				rows = append(rows, r)
			}
		}
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	named := 0
	for _, r := range rows {
		if r.Name != "" {
			named++
			printIdentity(r.Path, r.DocIdentity)
		}
	}
	for _, r := range rows {
		if r.Name == "" {
			fmt.Printf("%s\n    (no name — what this is has not been established)\n", r.Path)
		}
	}
	fmt.Printf("%d of %d document(s) named\n", named, len(rows))
	return nil
}

// printIdentity renders one document: the caption first, the filename beneath
// it. Both, always — the filename is what every citation already written joins
// on, and where the two disagree, that IS the finding.
func printIdentity(path string, d raglit.DocIdentity) {
	who := d.Model
	if d.ByPerson() {
		who = who + " (a person)"
	}
	fmt.Printf("%s\n    %s", d.Name, path)
	if d.Kind != "" {
		fmt.Printf("\n    kind: %s", d.Kind)
	}
	if len(d.ContentTags) > 0 {
		fmt.Printf("\n    about: %s", strings.Join(d.ContentTags, ", "))
	}
	if len(d.RoleTags) > 0 {
		fmt.Printf("\n    role:  %s", strings.Join(d.RoleTags, ", "))
	}
	if strings.TrimSpace(who) != "" {
		fmt.Printf("  ·  %s", strings.TrimSpace(who))
	}
	if d.Summary != "" {
		fmt.Printf("\n    %s", strings.ReplaceAll(d.Summary, "\n", "\n    "))
	}
	fmt.Println()
}

// daemonEnqueueIdentity queues one document with the daemon, returning how many
// rows it added (0 when one is already in flight for that path).
func daemonEnqueueIdentity(path string, force, tagsOnly bool) (int, error) {
	base, idx, dir, err := daemonTarget()
	if err != nil {
		return 0, err
	}
	q := urlValues("project", dir, "index", idx, "path", path)
	if force {
		q.Set("force", "true")
	}
	if tagsOnly {
		q.Set("tags_only", "true")
	}
	b, err := daemonPostJSON(base, "/api/identify/queue?"+q.Encode(), map[string]any{})
	if err != nil {
		return 0, err
	}
	var out struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	return out.Queued, nil
}

// daemonIdentityQueue reads the queue's counts from the daemon.
func daemonIdentityQueue() (raglit.IdentityQueueStatus, error) {
	var out struct {
		Queue raglit.IdentityQueueStatus `json:"queue"`
	}
	base, idx, _, err := daemonTarget()
	if err != nil {
		return out.Queue, err
	}
	b, err := daemonGet(base, "/api/identity-jobs", urlValues("index", idx, "limit", "1"))
	if err != nil {
		return out.Queue, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out.Queue, err
	}
	return out.Queue, nil
}

// daemonTarget resolves the three things every daemon call needs: the base URL,
// this project's namespaced index, and the project directory.
func daemonTarget() (base, index, dir string, err error) {
	d, ok := raglit.ProjectDir()
	if !ok {
		return "", "", "", fmt.Errorf("no .raglit/ found from here")
	}
	base, err = ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return "", "", "", err
	}
	index, err = daemonIndexName()
	if err != nil {
		return "", "", "", err
	}
	return base, index, d, nil
}

// daemonIdentify routes an identity write to the daemon. An empty want means
// "generate one"; a non-empty one records a person's.
func daemonIdentify(path string, want raglit.DocIdentity, by string, force bool) (raglit.DocIdentity, error) {
	base, idx, dir, err := daemonTarget()
	if err != nil {
		return raglit.DocIdentity{}, err
	}
	q := urlValues("project", dir, "index", idx, "path", path,
		"name", want.Name, "summary", want.Summary, "kind", want.Kind, "by", by)
	if force {
		q.Set("force", "true")
	}
	b, err := daemonPostJSON(base, "/api/identify?"+q.Encode(), map[string]any{})
	if err != nil {
		return raglit.DocIdentity{}, err
	}
	var out struct {
		Identity raglit.DocIdentity `json:"identity"`
		Kept     bool               `json:"kept"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return raglit.DocIdentity{}, err
	}
	if out.Kept {
		return out.Identity, raglit.ErrIdentityKept
	}
	return out.Identity, nil
}
