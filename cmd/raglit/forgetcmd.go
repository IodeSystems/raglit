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
	resolve := addTargetFlags(fs)
	dry := fs.Bool("dry-run", false, "name what would be removed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()
	if len(targets) == 0 {
		return fmt.Errorf("forget: name a document (or a directory prefix)")
	}
	t, err := resolve()
	if err != nil {
		return err
	}
	docs, err := t.documents()
	if err != nil {
		return err
	}
	paths := matchIndexed(docs, targets)
	if len(paths) == 0 {
		return fmt.Errorf("forget: nothing indexed under %s in %s", strings.Join(targets, ", "), t.local())
	}
	for _, p := range paths {
		fmt.Println(p)
		if *dry {
			continue
		}
		if err := daemonForgetIn(t.base, t.index, p); err != nil {
			return err
		}
	}
	verb := "forgot"
	if *dry {
		verb = "would forget"
	}
	fmt.Printf("%s %d document(s) from %s — files on your disk are untouched; uploaded files are deleted\n", verb, len(paths), t.local())
	return nil
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
