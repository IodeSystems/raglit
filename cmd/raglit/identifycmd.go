package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

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

	paths, err := identifyTargets(st, targets, *force)
	if err != nil {
		return err
	}
	if *limit > 0 && len(paths) > *limit {
		paths = paths[:*limit]
	}
	if len(paths) == 0 {
		fmt.Println("nothing to identify — every document has a name")
		return nil
	}
	if *dry {
		for _, p := range paths {
			fmt.Println(p)
		}
		fmt.Printf("would identify %d document(s)\n", len(paths))
		return nil
	}

	ctx := context.Background()
	done, kept, failed := 0, 0, 0
	for _, p := range paths {
		var id raglit.DocIdentity
		var err error
		if routed {
			id, err = daemonIdentify(p, raglit.DocIdentity{}, "", *force)
		} else {
			id, err = st.IdentifyDocument(ctx, p, *force)
		}
		switch {
		case errors.Is(err, raglit.ErrIdentityKept):
			kept++
		case errors.Is(err, raglit.ErrNoIdentifier):
			// Nothing further will work; say so once rather than N times.
			return fmt.Errorf("identify: no identity model configured — run 'raglit init' or set identity_model")
		case err != nil:
			// One document that cannot be captioned does not stop the corpus.
			// Named, though: a silent skip is how half a corpus ends up captioned
			// with nothing recording which half.
			failed++
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", p, err)
		default:
			done++
			printIdentity(p, id)
		}
	}
	fmt.Printf("identified %d document(s)", done)
	if kept > 0 {
		fmt.Printf(", kept %d", kept)
	}
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()
	return nil
}

// identifyTargets is the work list: the named documents, or every document with
// no caption yet (with --force, every document).
func identifyTargets(st *raglit.Store, targets []string, force bool) ([]string, error) {
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
	if strings.TrimSpace(who) != "" {
		fmt.Printf("  ·  %s", strings.TrimSpace(who))
	}
	if d.Summary != "" {
		fmt.Printf("\n    %s", strings.ReplaceAll(d.Summary, "\n", "\n    "))
	}
	fmt.Println()
}

// daemonIdentify routes an identity write to the daemon. An empty want means
// "generate one"; a non-empty one records a person's.
func daemonIdentify(path string, want raglit.DocIdentity, by string, force bool) (raglit.DocIdentity, error) {
	dir, ok := raglit.ProjectDir()
	if !ok {
		return raglit.DocIdentity{}, fmt.Errorf("no .raglit/ found from here")
	}
	base, err := ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return raglit.DocIdentity{}, err
	}
	idx, err := daemonIndexName()
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
