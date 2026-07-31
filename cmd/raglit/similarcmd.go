package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// runSimilar answers "do we already hold this?" for a candidate file, at page
// grain and in both directions.
//
// Built for an upload triage flow, so the OUTPUT SHAPE is the contract and
// --json is not an afterthought: the caller has to branch on Relation and read
// page ranges, and anything that only prints a table forces it to parse prose.
// The text rendering exists for a person reading the same answer.
//
// The interesting output is not a score. It is "pages 12-14 of your upload are
// pages 1-3 of a document we already hold", plus — when two copies of one
// instrument do not agree — WHICH numbers differ. A triage tool that collapses
// near-duplicates into one score cannot say either.
func runSimilar(args []string) error {
	fs := flag.NewFlagSet("similar", flag.ExitOnError)
	openStore, _ := addStoreFlags(fs)
	build := fs.Bool("build", false, "build missing page sketches, then exit")
	rebuild := fs.Bool("rebuild", false, "rebuild every page sketch (after a recipe change)")
	status := fs.Bool("status", false, "report sketch coverage, then exit")
	all := fs.Bool("all", false, "audit the whole index: compare every sketched document")
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	limit := fs.Int("n", 0, "max matches per probe (0 = all)")
	minChars := fs.Int("min-chars", 0, "minimum matched folded chars to report (0 = default)")
	minCov := fs.Float64("min-coverage", 0, "additional block-coverage floor (0 = none)")
	width := fs.Int("width", 0, "shingle width in folded chars (0 = default)")
	mod := fs.Int("mod", 0, "sample divisor for the stored sketch (0 = default)")
	self := fs.Bool("self", false, "keep the probe's own path in results")
	genericDF := fs.Int("generic-df", 0, "mask text occurring in at least this many documents (0 = default)")
	noMask := fs.Bool("no-mask", false, "do not mask corpus-generic text (diagnostic: shows what masking suppressed)")
	fs.Parse(args)

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	w, m := *width, *mod
	if w <= 0 {
		w = raglit.FoldWidth
	}
	if m <= 0 {
		m = raglit.SampleMod
	}

	if *status {
		st, err := store.SketchStatusFor(w, m)
		if err != nil {
			return err
		}
		if *asJSON {
			return emitJSON(st)
		}
		fmt.Printf("recipe:     %s\n", st.Recipe)
		fmt.Printf("documents:  %d (%d sketched, %d unsketched)\n", st.Documents, st.Sketched, len(st.Unsketched))
		fmt.Printf("pages:      %d\n", st.Pages)
		fmt.Printf("index rows: %d\n", st.IndexRows)
		if len(st.StaleRecipe) > 0 {
			fmt.Printf("\n%d document(s) sketched under a DIFFERENT recipe — rerun with --rebuild:\n", len(st.StaleRecipe))
			for _, p := range st.StaleRecipe {
				fmt.Printf("  %s\n", p)
			}
		}
		if len(st.Unsketched) > 0 {
			fmt.Printf("\n%d document(s) are UNCHECKED (no sketch). These are not clean, they are unread:\n", len(st.Unsketched))
			for _, p := range st.Unsketched {
				fmt.Printf("  %s\n", p)
			}
		}
		return nil
	}

	if *rebuild {
		// A recipe change invalidates every stored row, and the recipe column is
		// what makes that detectable. Clearing first rather than relying on the
		// per-document replace: a document deleted since the last build would
		// otherwise keep its rows forever and go on generating candidates.
		if err := store.ClearSketches(); err != nil {
			return err
		}
	}
	if *build || *rebuild {
		n, errs := store.SketchAll(w, m)
		fmt.Printf("sketched %d document(s) under %s\n", n, raglit.Recipe(w, m))
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  skipped: %v\n", e)
		}
		return nil
	}

	opts := raglit.SimilarOpts{
		Width: w, Mod: m, MinChars: *minChars, MinCoverage: *minCov, Limit: *limit, Self: *self,
		GenericDF: *genericDF, NoMask: *noMask,
	}

	if *all {
		return auditAll(store, opts, *asJSON)
	}

	if fs.NArg() == 0 {
		return fmt.Errorf("similar: give a file or an indexed document path (or --all, --status, --build)")
	}

	var reports []raglit.SimilarReport
	for _, target := range fs.Args() {
		rep, err := probeOne(store, target, opts)
		if err != nil {
			return err
		}
		reports = append(reports, rep)
	}
	if *asJSON {
		if len(reports) == 1 {
			return emitJSON(reports[0])
		}
		return emitJSON(reports)
	}
	for i, rep := range reports {
		if i > 0 {
			fmt.Println()
		}
		printReport(rep)
	}
	return nil
}

// probeOne resolves what to compare, in the order that costs least and keeps the
// answer honest:
//
//  1. An indexed document path — compare the index's own text, so the probe went
//     through the same OCR as everything it is compared against.
//  2. A raglit transcription sidecar beside the file — a previous ingest already
//     read this document, and reusing that read keeps the whole check offline.
//  3. Local extraction, no OCR. A born-digital PDF, an office file or text
//     extracts with no model.
//
// A scanned page with no sidecar and no OCR configured is refused, and says so,
// rather than comparing a blank extraction against the corpus and reporting the
// upload as novel. Silently returning "nothing similar" for a document nothing
// could read is the one answer this must not give: it tells the triage flow to
// accept a duplicate.
func probeOne(store *raglit.Store, target string, opts raglit.SimilarOpts) (raglit.SimilarReport, error) {
	if pages, err := store.TruePages(target); err == nil && len(pages) > 0 {
		return store.SimilarIndexed(target, opts)
	}
	// Exact bytes first, and BEFORE any read. A scanned PDF this index cannot OCR
	// still has a sha256, so the strongest answer is also the one available for the
	// input the shingle path has to refuse. It is also immune to the generic-text
	// masking that makes an identical pair score 0.93.
	var exact map[string]string
	if b, err := os.ReadFile(target); err == nil {
		if exact, err = store.SameBytesHash(raglit.HashHex(b)); err != nil {
			return raglit.SimilarReport{}, err
		}
	}
	withExact := func(rep raglit.SimilarReport, err error) (raglit.SimilarReport, error) {
		rep.Exact = exact
		return rep, err
	}
	sidecar := raglit.TranscriptionPath(target)
	if b, err := os.ReadFile(sidecar); err == nil {
		pages := raglit.ParseTranscription(string(b))
		if len(pages) > 0 {
			rep, err := store.SimilarTo(target, raglit.FoldPages(pages), opts)
			rep.Source = "transcription:" + filepath.Base(sidecar)
			return withExact(rep, err)
		}
	}
	if _, err := os.Stat(target); err != nil {
		return raglit.SimilarReport{}, fmt.Errorf(
			"similar: %q is neither an indexed document path nor a readable file", target)
	}
	pages, err := raglit.ExtractPaged(context.Background(), target, nil)
	if err != nil {
		// An exact-bytes hit still answers the question, so report it rather than
		// failing: "we already hold these exact bytes" needs no text at all, and
		// refusing here would send an upload for OCR that did not need reading.
		if len(exact) > 0 {
			return withExact(raglit.SimilarReport{Probe: target, Source: "bytes-only"}, nil)
		}
		return raglit.SimilarReport{}, fmt.Errorf(
			"similar: cannot read %s without OCR (%w)\n"+
				"  index it first (raglit index %s), or run raglit transcribe --write %s",
			target, err, target, target)
	}
	rep, err := store.SimilarTo(target, raglit.FoldPages(pages), opts)
	rep.Source = "extracted"
	return withExact(rep, err)
}

// auditAll compares every sketched document against every other.
//
// The point is a corpus census, not a per-upload check: "which instruments do we
// hold more than once, and do the copies agree?". Pairs are reported once, in a
// canonical direction (container second), because a symmetric listing of 115
// documents reads as 230 findings that are 115 facts.
func auditAll(store *raglit.Store, opts raglit.SimilarOpts, asJSON bool) error {
	st, err := store.SketchStatusFor(opts.Width, opts.Mod)
	if err != nil {
		return err
	}
	if st.Sketched == 0 {
		return fmt.Errorf("similar --all: nothing is sketched yet — run 'raglit similar --build' first")
	}
	if len(st.Unsketched) > 0 {
		fmt.Fprintf(os.Stderr,
			"warning: %d of %d documents are UNSKETCHED and were not compared — run --build\n",
			len(st.Unsketched), st.Documents)
	}
	docs, err := store.SketchedPaths(opts.Width, opts.Mod)
	if err != nil {
		return err
	}
	type pair struct {
		A, B string
		M    raglit.DocMatch
	}
	var pairs []pair
	seen := map[string]bool{}
	for _, p := range docs {
		rep, err := store.SimilarIndexed(p, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skipped %s: %v\n", p, err)
			continue
		}
		for _, m := range rep.Matches {
			key := p + "\x00" + m.Path
			rev := m.Path + "\x00" + p
			if seen[key] || seen[rev] {
				continue
			}
			seen[key] = true
			pairs = append(pairs, pair{A: p, B: m.Path, M: m})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		ki := maxf(pairs[i].M.BlockCoverProbe, pairs[i].M.BlockCoverMatch)
		kj := maxf(pairs[j].M.BlockCoverProbe, pairs[j].M.BlockCoverMatch)
		if ki != kj {
			return ki > kj
		}
		return pairs[i].A < pairs[j].A
	})
	if asJSON {
		return emitJSON(pairs)
	}
	fmt.Printf("%d document(s) compared, %d overlapping pair(s)\n\n", len(docs), len(pairs))
	for _, pr := range pairs {
		fmt.Printf("%-18s  %s\n", pr.M.Relation, pr.A)
		fmt.Printf("%-18s  %s\n", "", pr.B)
		fmt.Printf("  jaccard %.3f   contains: A→B %.3f  B→A %.3f   coverage: A %.3f  B %.3f\n",
			pr.M.Jaccard, pr.M.ContainProbe, pr.M.ContainMatch, pr.M.BlockCoverProbe, pr.M.BlockCoverMatch)
		printBlocks(pr.M, "  ")
		printDisagreement(pr.M, "  ")
		fmt.Println()
	}
	return nil
}

func printReport(rep raglit.SimilarReport) {
	fmt.Printf("probe: %s\n", rep.Probe)
	src := rep.Source
	if rep.Indexed {
		src = "index"
	}
	fmt.Printf("  read from %s — %d page(s), %d folded chars, %d distinct shingles (%s)\n",
		src, rep.Pages, rep.Chars, rep.Shingled, rep.Recipe)
	if len(rep.Exact) > 0 {
		fmt.Printf("\n  IDENTICAL BYTES — the index already holds this exact file as:\n")
		paths := make([]string, 0, len(rep.Exact))
		for p := range rep.Exact {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fmt.Printf("    %s\n", p)
		}
		fmt.Println("  A filename that disagrees with one of these is misfiled, not a second copy.")
	}
	if rep.Shingled == 0 && len(rep.Exact) == 0 {
		fmt.Println("\n  UNCHECKABLE: this document holds less text than one shingle.")
		fmt.Println("  That is not 'nothing similar found' — nothing was compared. A blank scan,")
		fmt.Println("  or a page that is entirely figure. Read it before accepting it.")
		return
	}
	if len(rep.Matches) == 0 {
		fmt.Println("\n  no overlap found — this document appears novel to the index")
		return
	}
	for _, m := range rep.Matches {
		fmt.Printf("\n  %s  %s\n", m.Relation, m.Path)
		if m.Title != "" {
			fmt.Printf("    %s\n", m.Title)
		}
		fmt.Printf("    jaccard %.3f   probe⊂match %.3f   match⊂probe %.3f\n",
			m.Jaccard, m.ContainProbe, m.ContainMatch)
		fmt.Printf("    coverage: probe %.3f  match %.3f   (%d/%d shared shingles)\n",
			m.BlockCoverProbe, m.BlockCoverMatch, m.SharedShingles, m.ProbeShingles)
		printBlocks(m, "    ")
		printDisagreement(m, "    ")
	}
}

// maxBlocksShown caps the alignment listing. A pair of long filings sharing a
// caption on every page produces dozens of blocks; the ones that carry the
// relation are the longest, and they are sorted first.
const maxBlocksShown = 6

func printBlocks(m raglit.DocMatch, ind string) {
	if len(m.Blocks) == 0 {
		return
	}
	fmt.Printf("%saligned:\n", ind)
	for i, b := range m.Blocks {
		if i == maxBlocksShown {
			fmt.Printf("%s  … %d more block(s)\n", ind, len(m.Blocks)-maxBlocksShown)
			break
		}
		agree := ""
		// Only worth saying when the two copies are NOT identical through the
		// block. "agreement 1.000" on every line is noise that hides the one line
		// where it is 0.83.
		if b.Agreement() < 0.999 {
			agree = fmt.Sprintf("   agreement %.3f in %d runs", b.Agreement(), b.Runs)
		}
		fmt.Printf("%s  probe %s = match %s   %d chars%s\n", ind,
			pageRange(b.ProbeFromPage, b.ProbeToPage), pageRange(b.MatchFromPage, b.MatchToPage),
			b.MatchedChars, agree)
		for _, g := range b.Gaps {
			fmt.Printf("%s    differs at probe p%d / match p%d:\n", ind, g.ProbePage, g.MatchPage)
			fmt.Printf("%s      probe: %s\n", ind, g.ProbeText)
			fmt.Printf("%s      match: %s\n", ind, g.MatchText)
		}
	}
}

// printDisagreement leads with the numbers, because in a legal corpus that is the
// finding. Two copies of one deed that align at 0.97 and disagree about a distance
// are either an altered exhibit or a different version filed, and either is worth
// a person's attention in a way that "0.97" is not.
func printDisagreement(m raglit.DocMatch, ind string) {
	if len(m.NumericOnlyInProbe) == 0 && len(m.NumericOnlyInMatch) == 0 {
		return
	}
	fmt.Printf("%sNUMBERS DIFFER inside the aligned text — these two copies are not the same text:\n", ind)
	if len(m.NumericOnlyInProbe) > 0 {
		fmt.Printf("%s  only in probe: %s\n", ind, strings.Join(cap20(m.NumericOnlyInProbe), " "))
	}
	if len(m.NumericOnlyInMatch) > 0 {
		fmt.Printf("%s  only in match: %s\n", ind, strings.Join(cap20(m.NumericOnlyInMatch), " "))
	}
}

func cap20(v []string) []string {
	if len(v) <= 20 {
		return v
	}
	return append(append([]string{}, v[:20]...), fmt.Sprintf("(+%d more)", len(v)-20))
}

func pageRange(from, to int) string {
	if from == to {
		return fmt.Sprintf("p%d", from)
	}
	return fmt.Sprintf("p%d-%d", from, to)
}

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
