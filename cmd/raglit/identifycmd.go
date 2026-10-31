package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
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
	resolve := addTargetFlags(fs)
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
	t, err := resolve()
	if err != nil {
		return err
	}
	if *list {
		return printIdentities(t, targets, *asJSON)
	}

	// A person's ruling: one document, recorded, done.
	if strings.TrimSpace(*name) != "" || strings.TrimSpace(*summary) != "" || strings.TrimSpace(*kind) != "" {
		if len(targets) != 1 {
			return fmt.Errorf("identify: name exactly one document to record an identity for")
		}
		path, err := t.oneDoc(targets[0])
		if err != nil {
			return fmt.Errorf("identify: %w", err)
		}
		got, err := daemonIdentify(t, path, raglit.DocIdentity{Name: *name, Summary: *summary, Kind: *kind}, *by, false)
		if err != nil {
			return err
		}
		printIdentity(path, got)
		return nil
	}

	paths, err := identifyTargets(t, targets, *force, *tagsOnly)
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
	mode := ""
	if *tagsOnly {
		mode = raglit.IdentityAskTags
	}
	queued := 0
	for _, p := range paths {
		n, err := daemonEnqueueAsk(t, p, *force, mode)
		if err != nil {
			return err
		}
		queued += n
	}
	q, _ := daemonIdentityQueue(t)
	fmt.Printf("queued %d document(s) — %d pending, %d running, %d captioned, %d skipped, %d failed\n",
		queued, q.Pending, q.Running, q.Done, q.Skipped, q.Failed)
	if !*wait {
		fmt.Println("the daemon is working them; `raglit identify --wait` follows, `--list` shows what is named")
		return nil
	}
	return waitForIdentityQueue(t)
}

// waitForIdentityQueue follows the daemon's progress until the queue is empty.
// Interrupting it stops the watching, not the work.
func waitForIdentityQueue(t target) error {
	last := ""
	for {
		q, err := daemonIdentityQueue(t)
		if err != nil {
			return err
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
func identifyTargets(t target, targets []string, force, tagsOnly bool) ([]string, error) {
	if len(targets) > 0 {
		docs, err := t.documents()
		if err != nil {
			return nil, err
		}
		paths := matchIndexed(docs, targets)
		if len(paths) == 0 {
			return nil, fmt.Errorf("identify: nothing indexed under %s", strings.Join(targets, ", "))
		}
		return paths, nil
	}
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	if tagsOnly {
		q.Set("mode", raglit.IdentityAskTags)
	}
	var out struct {
		Paths []string `json:"paths"`
	}
	if err := t.get("/api/identify/targets", q, &out); err != nil {
		return nil, err
	}
	return out.Paths, nil
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
func printIdentities(t target, targets []string, asJSON bool) error {
	all, err := t.identities()
	if err != nil {
		return err
	}
	rows := all
	if len(targets) > 0 {
		docs, err := t.documents()
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

// daemonEnqueueAsk queues one document's ask (identity, tags or fields) with
// the daemon, returning how many rows it added (0 when one is already in
// flight for that path).
func daemonEnqueueAsk(t target, path string, force bool, mode string) (int, error) {
	q := urlValues("project", projectDirOf(t.home), "index", t.index, "path", path)
	if force {
		q.Set("force", "true")
	}
	if mode != "" {
		q.Set("mode", mode)
	}
	b, err := daemonPostJSON(t.base, "/api/identify/queue?"+q.Encode(), map[string]any{})
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
func daemonIdentityQueue(t target) (raglit.IdentityQueueStatus, error) {
	var out struct {
		Queue raglit.IdentityQueueStatus `json:"queue"`
	}
	err := t.get("/api/identity-jobs", url.Values{"limit": {"1"}}, &out)
	return out.Queue, err
}

// daemonIdentify routes an identity write to the daemon. An empty want means
// "generate one"; a non-empty one records a person's.
func daemonIdentify(t target, path string, want raglit.DocIdentity, by string, force bool) (raglit.DocIdentity, error) {
	q := urlValues("project", projectDirOf(t.home), "index", t.index, "path", path,
		"name", want.Name, "summary", want.Summary, "kind", want.Kind, "by", by)
	if force {
		q.Set("force", "true")
	}
	b, err := daemonPostJSON(t.base, "/api/identify?"+q.Encode(), map[string]any{})
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
