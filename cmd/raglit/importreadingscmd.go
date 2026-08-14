package main

import (
	"flag"
	"fmt"
	"os"
)

// `raglit import-readings` adopts verified transcripts already in the corpus as
// ATTESTED readings of the recordings they transcribe.
//
// A command, and a dry run by default, for the reason the attachment migration
// is: it changes what search answers with, and it does so by matching on text
// rather than on a declared link — because the link oidio would have declared
// stops one hop short of the recording.
func runImportReadings(args []string) error {
	fs := flag.NewFlagSet("import-readings", flag.ExitOnError)
	openStore, _ := addStoreFlags(fs)
	apply := fs.Bool("apply", false, "record the readings (default: report what would be adopted)")
	by := fs.String("by", "", "who ruled on these transcripts")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: raglit import-readings [--index NAME] [--apply] [--by NAME]

A verified transcript beside a recording is a READING of that recording, not a
separate document. Indexed as its own document it makes one hearing appear twice
in search, with nothing to say which one may be quoted.

This finds those transcripts and records them as attested readings of the
recording they match, so search collapses to one — the one a person ruled on.

Matching is by CONTENT: the transcripts of one recording share almost every
word. Anything below the floor is reported and left alone rather than guessed at,
because a wrong match ranks a transcript above the wrong hearing.
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	matches, err := store.ImportVerifiedTranscripts(!*apply, *by)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		fmt.Println("no verified transcripts found that are not already readings")
		return nil
	}
	adopted := 0
	for _, m := range matches {
		switch {
		case m.Why != "":
			fmt.Printf("  SKIPPED %s\n          %s\n", base(m.Transcript), m.Why)
		default:
			adopted++
			verb := "would adopt"
			if *apply {
				verb = "adopted"
			}
			fmt.Printf("  %s %s\n      as an attested reading of %s (%.0f%% of its words)\n",
				verb, base(m.Transcript), base(m.Recording), m.Score*100)
		}
	}
	fmt.Printf("\n%d of %d transcript(s) matched a recording.\n", adopted, len(matches))
	if !*apply && adopted > 0 {
		fmt.Println("Nothing was changed. Re-run with --apply.")
	}
	return nil
}

func base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
