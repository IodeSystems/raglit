package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/iodesystems/raglit"
)

// `raglit about` — what this INDEX is, as opposed to what a document is.
//
// Two answers, and the command shows both because they fail differently. The
// digest is counted from what is stored, so it is never wrong and never reads
// well. The paragraph reads well and is a model's account of the captions,
// which are themselves a model's account of the documents — two paraphrases
// deep, and therefore marked as generated wherever it is shown.
func runAbout(args []string) error {
	fs := flag.NewFlagSet("about", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	write := fs.Bool("write", false, "ask the model to (re)write the paragraph")
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	lf.resolve(homeOf())

	if *write {
		id := lf.identifier(homeOf())
		if id == nil {
			return fmt.Errorf("about: no identity model configured — run 'raglit init' or set identity_model")
		}
		st.SetIdentifier(id)
		if _, err := st.WriteIndexAbout(context.Background()); err != nil {
			return err
		}
	}

	d, err := st.IndexDigest()
	if err != nil {
		return err
	}
	if *asJSON {
		b, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("%d document(s)", d.Documents)
	if d.Untagged > 0 {
		fmt.Printf(", %d untagged", d.Untagged)
	}
	fmt.Println()
	if d.About != "" {
		fmt.Println()
		if d.AboutStale {
			fmt.Println("  (generated — and behind the corpus; `raglit about --write` refreshes it)")
		} else {
			fmt.Println("  (generated from the captions, not the documents)")
		}
		for _, line := range wrapAt(d.About, 76) {
			fmt.Println("  " + line)
		}
	} else if !*write {
		fmt.Println("\n  no summary written yet — `raglit about --write` asks the model for one")
	}
	if len(d.Kinds) > 0 {
		fmt.Printf("\nkinds:   %s\n", raglit.TagLine(d.Kinds))
	}
	if len(d.Content) > 0 {
		fmt.Printf("about:   %s\n", raglit.TagLine(d.Content))
	}
	if len(d.Roles) > 0 {
		fmt.Printf("roles:   %s\n", raglit.TagLine(d.Roles))
	}
	return nil
}

// wrapAt breaks a paragraph at word boundaries for a terminal.
func wrapAt(s string, width int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) <= width:
			line += " " + w
		default:
			out = append(out, line)
			line = w
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
