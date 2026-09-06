package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/iodesystems/raglit"
)

// runDiff compares two transcripts page by page.
//
// `similar` answers "does the corpus already hold this" across the whole corpus
// and reports one number per pair. This answers a narrower question about two
// named documents — "are these the same filing" — and it needs a different shape
// of answer, because a whole-document 0.93 is equally consistent with a clean
// second scan and with a re-recorded instrument whose page 3 changed.
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	asJSON := fs.Bool("json", false, "emit the diff as JSON")
	width := fs.Int("width", 0, "shingle width in folded chars (0 = default)")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("diff: want two indexed document paths")
	}
	aPath, bPath := fs.Arg(0), fs.Arg(1)

	store, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer store.Close()

	w := *width
	if w <= 0 {
		w = raglit.FoldWidth
	}

	// Byte identity first, because it ends the question. Reading a page table to
	// decide whether two files are the same document, when their hashes already
	// say so, is work done to reach an answer already in hand.
	var d raglit.DocDiff
	if same, err := store.SameBytesAs(aPath); err == nil {
		if _, ok := same[bPath]; ok {
			d.SameBytes = true
		}
	}

	aPages, err := store.TruePages(aPath)
	if err != nil {
		return fmt.Errorf("diff: %s: %w", aPath, err)
	}
	bPages, err := store.TruePages(bPath)
	if err != nil {
		return fmt.Errorf("diff: %s: %w", bPath, err)
	}
	if len(aPages) == 0 || len(bPages) == 0 {
		return fmt.Errorf("diff: one of the documents has no indexed text (unsketched or unread)")
	}
	af, bf := raglit.FoldPages(aPages), raglit.FoldPages(bPages)

	// No mask. Corpus-generic masking exists to stop shared boilerplate inflating
	// a SEARCH across hundreds of documents; here the two documents are named by
	// the caller, and hiding the text they share would remove the evidence the
	// comparison is for.
	d.Match = raglit.Compare(af, bf, w, nil, nil)
	d.Pages, d.OnlyInB = raglit.PageRates(af, bf, w)
	d.Shape = raglit.Shape(d.Pages)

	if *asJSON {
		return emitJSON(d)
	}
	printDiff(aPath, bPath, d)
	return nil
}

func printDiff(aPath, bPath string, d raglit.DocDiff) {
	fmt.Printf("A: %s\nB: %s\n\n", aPath, bPath)
	if d.SameBytes {
		fmt.Println("IDENTICAL BYTES — the same file under two paths. Nothing below can disagree with that.")
		fmt.Println()
	}

	s := d.Shape
	fmt.Printf("%-14s %s\n", "relation", d.Match.Relation)
	fmt.Printf("%-14s A %.3f   B %.3f\n", "coverage", d.Match.BlockCoverProbe, d.Match.BlockCoverMatch)
	fmt.Printf("%-14s %d page(s): %d identical, %d noisy, %d divergent, %d missing\n\n",
		"pages", s.Pages, s.Clean, s.Noisy, s.Divergent, s.Missing)

	fmt.Printf("  %-6s %-6s %-8s %s\n", "A pg", "B pg", "rate", "")
	for _, p := range d.Pages {
		b, note := fmt.Sprintf("%d", p.BPage), ""
		switch {
		case p.BPage == 0 || p.Rate == 0:
			b, note = "—", "NOT IN B"
		case p.Identical:
			note = "identical"
		case p.Rate >= raglit.NoisyFloor:
			note = "same page, read differently"
		default:
			note = "DIFFERENT"
		}
		fmt.Printf("  %-6d %-6s %-8.3f %s\n", p.APage, b, p.Rate, note)
	}
	if len(d.OnlyInB) > 0 {
		fmt.Printf("\n  pages in B that nothing in A matched: %v\n", d.OnlyInB)
	}

	// The disagreement itself, which is what decides copy versus version.
	printDisagreement(d.Match, "\n")

	fmt.Println()
	p := raglit.Propose(d.Match)
	kind := string(p.Kind)
	if kind == "" {
		kind = "open"
	}
	fmt.Printf("reads as: %s — %s\n", kind, p.Why)
	if s.Clean > 0 && s.Divergent > 0 {
		// The shape that matters most, said in words: agreement everywhere except
		// in specific places is what a refiling looks like, and an average hides it.
		fmt.Printf("  shape: %d page(s) match exactly and %d differ — disagreement is LOCALISED,\n"+
			"         which is a refiling rather than a second scan of one document.\n", s.Clean, s.Divergent)
	} else if s.Noisy > 0 && s.Divergent == 0 && s.Clean == 0 {
		fmt.Printf("  shape: every page disagrees a little and none disagrees a lot — diffuse,\n" +
			"         which is how a re-scan reads, not a refiling.\n")
	}
	if !d.SameBytes {
		fmt.Fprintf(os.Stderr, "\nrule it: raglit mark %q %q <copy|version>\n", aPath, bPath)
	}
}
