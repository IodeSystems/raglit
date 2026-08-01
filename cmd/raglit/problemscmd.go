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
		for _, p := range mine {
			fmt.Printf("  %s", p.Subject)
			if p.JobID > 0 {
				fmt.Printf("  #%d", p.JobID)
			}
			if p.Stage != "" {
				fmt.Printf("  [%s]", p.Stage)
			}
			fmt.Println()
			if d := strings.TrimSpace(p.Detail); d != "" {
				fmt.Printf("      %s\n", clip(strings.ReplaceAll(d, "\n", " "), 160))
			}
			if p.Fix != "" {
				fmt.Printf("      fix: %s\n", p.Fix)
			}
		}
	}
	return exitIfBroken(ps)
}

// exitIfBroken makes this usable in a check. A withdrawal is not a fault — it is
// a decision somebody made — so it never fails the run.
func exitIfBroken(ps []raglit.Problem) error {
	n := 0
	for _, p := range ps {
		if p.Kind != raglit.ProblemWithdrawn {
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
