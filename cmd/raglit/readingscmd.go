package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
)

// Showing every version of what a page says.
//
// A correction is an attestation that a better reading exists, not an erasure —
// so the readings accumulate and the old ones are kept. That is only worth doing
// if somebody can see them. Without this the history is in the index and
// reachable by sqlite3, which is the same as not having it.
//
// The question it answers is "what did this page say before, and who changed
// it": a quotation taken from a document last year matched the reading in force
// then, and if that reading has since been superseded, the quotation may be
// verbatim from text nothing holds any more.
func runReadings(args []string) error {
	fs := flag.NewFlagSet("readings", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	asJSON := fs.Bool("json", false, "emit as JSON")
	full := fs.Bool("text", false, "print the full text of each reading, not a preview")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("readings: name a document (and optionally a page)")
	}
	doc, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	page := 0
	if len(pos) > 1 {
		if _, err := fmt.Sscanf(pos[1], "%d", &page); err != nil || page < 1 {
			return fmt.Errorf("readings: %q is not a page number", pos[1])
		}
	}

	store, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()

	var all []raglit.PageReading
	if page > 0 {
		if all, err = store.PageReadings(ctx, doc, page); err != nil {
			return err
		}
	} else {
		// Every page of the document that has more than the reading it was born
		// with. Pages nobody touched are not interesting here and would bury the
		// ones that were.
		sup, err := store.SupersededPages(ctx)
		if err != nil {
			return err
		}
		seen := map[int]bool{}
		for _, r := range sup {
			if r.Doc == doc && !seen[r.Page] {
				seen[r.Page] = true
				rs, err := store.PageReadings(ctx, doc, r.Page)
				if err != nil {
					return err
				}
				all = append(all, rs...)
			}
		}
	}

	if *asJSON {
		return emitJSON(all)
	}
	if len(all) == 0 {
		if page > 0 {
			fmt.Printf("no recorded readings for page %d of %s\n", page, filepath.Base(doc))
		} else {
			fmt.Printf("no page of %s has a superseded reading\n", filepath.Base(doc))
		}
		fmt.Println("  a page gets a history the first time somebody corrects it:")
		fmt.Println("    raglit transcribe --correct --page N <doc> < corrected.txt")
		return nil
	}

	fmt.Printf("%s\n\n", doc)
	lastPage, prev := -1, ""
	for _, r := range all {
		if r.Page != lastPage {
			if lastPage != -1 {
				fmt.Println()
			}
			fmt.Printf("page %d\n", r.Page)
			lastPage, prev = r.Page, ""
		}
		mark := " "
		if r.Active {
			mark = "*"
		}
		who := r.By
		if who == "" {
			who = r.Source
		}
		fmt.Printf("  %s v%d  %-10s %-14s %-11s %6d chars\n", mark, r.Seq, r.Source, who, r.At, len(r.Text))
		if r.Note != "" {
			fmt.Printf("      %s\n", r.Note)
		}
		// What actually changed. A version list without it makes a reader diff by
		// eye, and the whole reason a reading was replaced is usually one line.
		if prev != "" && prev != r.Text {
			if a, b := firstDifference(prev, r.Text); a != "" || b != "" {
				fmt.Printf("      was: %s\n", trim1(a))
				fmt.Printf("      now: %s\n", trim1(b))
			}
		}
		if *full {
			fmt.Printf("      ---\n%s\n      ---\n", r.Text)
		}
		prev = r.Text
	}
	fmt.Printf("\n* = the reading in force. Superseded readings are kept: a quotation taken\n")
	fmt.Printf("  before a correction matched the text in force then, not the text now.\n")
	return nil
}

// firstDifference returns the first line that differs between two readings.
func firstDifference(a, b string) (string, string) {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			return x, y
		}
	}
	return "", ""
}

func trim1(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 88 {
		return s[:88] + "…"
	}
	if s == "" {
		return "(nothing)"
	}
	return s
}
