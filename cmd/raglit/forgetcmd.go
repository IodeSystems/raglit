package main

import (
	"encoding/json"
	"fmt"
	"strings"

	flag "github.com/spf13/pflag"
)

// `raglit forget` removes a document from the INDEX, with its page images. A
// file on the user's disk is untouched; an upload's file goes with it, since the
// index is its only home (Store.RemoveDocument).
//
// Deliberately not `withdraw`, and the difference is the point. A withdrawal is
// a judgement — this document does not belong in the corpus — so it demands
// grounds, records them in the project's audit trail, and makes the ingest path
// refuse the document forever. That is exactly right for evidence somebody ruled
// out, and exactly wrong for raglit's own droppings: eight transcription
// sidecars indexed as documents are not a decision anybody made, and writing
// eight entries about tooling into a legal case's trail would be noise in the
// one place noise is expensive.
//
// So this records nothing and prevents nothing. The row goes; if the path is
// still a source the next sync re-indexes it, which is the correct behaviour for
// a mistake as opposed to a ruling.
func runForget(args []string) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	client := addClientFlags(fs) // --daemon, --embedded, --project: the same routing as ingest
	dry := fs.Bool("dry-run", false, "name what would be removed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()
	if len(targets) == 0 {
		return fmt.Errorf("forget: name a document (or a directory prefix)")
	}
	// Named a daemon or a project: forget from THAT project's --index on the
	// daemon, exactly where `ingest` with the same flags put it. Without
	// this, a client that ingested with --project could not take the
	// document back out: forget had no --project, and its routed path
	// below finds the project from a .raglit/ directory and always uses
	// the default index. Targets are exact document paths here — listing
	// the daemon's documents to match prefixes is not something it offers.
	if flagSet(fs, "project") || flagSet(fs, "daemon") {
		if flagSet(fs, "embedded") {
			return fmt.Errorf("forget: --embedded with --project/--daemon names two stores")
		}
		dURL, ns, err := client(homeOf, false)
		if err != nil {
			return err
		}
		idx, err := nsWriteIndex(ns, projectShared(homeOf), resolveIndexName(fs.Lookup("index").Value.String(), homeOf))
		if err != nil {
			return err
		}
		for _, p := range targets {
			fmt.Println(p)
			if *dry {
				continue
			}
			if err := daemonForgetIn(dURL, idx, p); err != nil {
				return err
			}
		}
		verb := "forgot"
		if *dry {
			verb = "would forget"
		}
		fmt.Printf("%s %d document(s) from %s — files on your disk are untouched; uploaded files are deleted\n", verb, len(targets), idx)
		return nil
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	docs, err := st.Documents()
	if err != nil {
		return err
	}
	paths := matchIndexed(docs, targets)
	if len(paths) == 0 {
		return fmt.Errorf("forget: nothing indexed under %s", strings.Join(targets, ", "))
	}
	ns, _ := resolveProject("", homeOf)
	routed := ns != "" && !explicitStoreFlag(fs) && !flagSet(fs, "embedded")
	for _, p := range paths {
		fmt.Println(p)
		if *dry {
			continue
		}
		if routed {
			// The daemon owns its indexes; a CLI deleting rows behind it is the
			// second writer, and on this layout it would delete from a different
			// index than the one search reads.
			if err := daemonForget(p); err != nil {
				return err
			}
			continue
		}
		if err := st.RemoveDocument(p, true); err != nil {
			return err
		}
	}
	verb := "forgot"
	if *dry {
		verb = "would forget"
	}
	fmt.Printf("%s %d document(s) — files on your disk are untouched; uploaded files are deleted\n", verb, len(paths))
	return nil
}

// flagSet reports whether a flag was given on the command line.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// daemonForgetIn drops one document from a named daemon index.
func daemonForgetIn(base, idx, path string) error {
	b, err := daemonPostJSON(base, "/api/forget?"+urlValues("index", idx, "path", path).Encode(), map[string]any{})
	if err != nil {
		return err
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("forget %s: the daemon did not confirm", path)
	}
	return nil
}

// daemonForget asks the daemon to drop a document from its index.
func daemonForget(path string) error {
	base, idx, dir, err := daemonTarget()
	if err != nil {
		return err
	}
	q := urlValues("project", dir, "index", idx, "path", path)
	b, err := daemonPostJSON(base, "/api/forget?"+q.Encode(), map[string]any{})
	if err != nil {
		return err
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("forget %s: the daemon did not confirm", path)
	}
	return nil
}
