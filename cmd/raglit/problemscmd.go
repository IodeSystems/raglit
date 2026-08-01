package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/iodesystems/raglit"
)

// `raglit health` — what is wrong with the corpus, in one command.
//
// The same report the UI renders, because the answer should not depend on which
// one you happen to be looking at. Exits non-zero when something is wrong, so it
// is usable as a check rather than only as a page to read.
func runHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable")
	kind := fs.String("kind", "", "only this kind (no-fragments, no-pages, job-failed, segment-degraded, llm-retries, withdrawn)")
	quiet := fs.Bool("quiet", false, "print nothing; exit non-zero if anything is wrong")
	openStore, homeOf := addStoreFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()

	ps, err := st.Problems(context.Background())
	if err != nil {
		return err
	}
	if *kind != "" {
		var f []raglit.Problem
		for _, p := range ps {
			if string(p.Kind) == *kind {
				f = append(f, p)
			}
		}
		ps = f
	}
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(ps); err != nil {
			return err
		}
		return exitIfBroken(ps)
	}
	if *quiet {
		return exitIfBroken(ps)
	}
	if len(ps) == 0 {
		fmt.Println("nothing wrong with this index")
		return nil
	}
	// Grouped, in the order the kinds are declared — by consequence, so the
	// things that lose data are read first.
	for _, k := range healthOrder {
		var mine []raglit.Problem
		for _, p := range ps {
			if p.Kind == k.kind {
				mine = append(mine, p)
			}
		}
		if len(mine) == 0 {
			continue
		}
		fmt.Printf("\n%s (%d)\n  %s\n", k.title, len(mine), k.why)
		// One reason, printed once. A bulk withdrawal gives twenty-six rows the
		// same grounds, and repeating them turns the only section a reader should
		// be able to skim into a wall — which is how a report stops being read.
		if shared := sharedDetail(mine); shared != "" {
			fmt.Printf("  reason: %s\n", clip(oneLine(shared), 200))
		}
		for _, p := range mine {
			fmt.Printf("  %s", p.Subject)
			if p.JobID > 0 {
				fmt.Printf("  #%d", p.JobID)
			}
			if p.Stage != "" {
				fmt.Printf("  [%s]", p.Stage)
			}
			fmt.Println()
			if d := strings.TrimSpace(p.Detail); d != "" && sharedDetail(mine) == "" {
				fmt.Printf("      %s\n", clip(oneLine(d), 160))
			}
			if p.Fix != "" {
				fmt.Printf("      fix: %s\n", p.Fix)
			}
		}
	}
	return exitIfBroken(ps)
}

// sharedDetail returns the detail every row in a group has in common, or "" when
// they differ. What makes a bulk withdrawal readable.
func sharedDetail(ps []raglit.Problem) string {
	if len(ps) < 2 {
		return ""
	}
	first := strings.TrimSpace(ps[0].Detail)
	if first == "" {
		return ""
	}
	for _, p := range ps[1:] {
		if strings.TrimSpace(p.Detail) != first {
			return ""
		}
	}
	return first
}

// exitIfBroken makes this usable in a check, and only FAULTS fail it.
//
// A withdrawal is a decision somebody made. A retried job completed — the retry
// row is the earliest warning of a failure to come, which is worth reading and
// is not itself a failure. Failing on either means the check is red from the
// first backpressure spike and stays red, and a check that is always red is one
// nobody looks at — the exact habit this whole report exists to break.
func exitIfBroken(ps []raglit.Problem) error {
	n := 0
	for _, p := range ps {
		switch p.Kind {
		case raglit.ProblemWithdrawn, raglit.ProblemRetries:
		default:
			n++
		}
	}
	if n > 0 {
		return fmt.Errorf("%d problem(s)", n)
	}
	return nil
}

var healthOrder = []struct {
	kind       raglit.ProblemKind
	title, why string
}{
	{raglit.ProblemNoFragments, "Indexed but unsearchable",
		"a document row exists and the count includes it, but no search can return it"},
	{raglit.ProblemNoPages, "Paged document with no pages",
		"page images, the transcript and every page citation have nothing behind them"},
	{raglit.ProblemJobFailed, "Failed ingest jobs",
		"the document is not in the index and nothing said so"},
	{raglit.ProblemDegraded, "Stored whole, not segmented",
		"the model would not segment these pages; searchable, but not at fragment grain"},
	{raglit.ProblemRetries, "Jobs the endpoint made fight",
		"completed, but only after retrying — the earliest warning of a failure to come"},
	{raglit.ProblemWithdrawn, "Withdrawn on purpose",
		"absent by decision, with grounds — not by accident"},
}
