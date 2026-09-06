package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
)

// `raglit forget` removes a document from the INDEX. The file on disk is
// untouched.
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
	dry := fs.Bool("dry-run", false, "name what would be removed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()
	if len(targets) == 0 {
		return fmt.Errorf("forget: name a document (or a directory prefix)")
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
	routed := ns != "" && !explicitStoreFlag(fs)
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
		if err := st.DeleteDocument(p); err != nil {
			return err
		}
	}
	verb := "forgot"
	if *dry {
		verb = "would forget"
	}
	fmt.Printf("%s %d document(s) — the files on disk are untouched\n", verb, len(paths))
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
