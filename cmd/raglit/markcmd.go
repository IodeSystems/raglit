package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// Ruling on what an overlapping pair IS.
//
// `raglit similar` measures overlap and stops, because the measurement cannot
// finish the job: a scan of a deed already held and a re-recorded deed both
// align at 0.97, and telling them apart decides whether a document is redundant
// or is the amendment that governs. raglit proposes; a person rules; the ruling
// is what gets stored.
//
// The two verbs mirror `kg attest`, which already solved this shape in this
// corpus: `marks --todo` is the queue of pairs nobody has ruled on, `mark` is
// the ruling. Rulings go to relations.jsonl beside the documents — never into
// .raglit/, which is gitignored and rebuilt.

func openRelations() (*raglit.Relations, error) {
	dir, ok := raglit.ProjectDir()
	if !ok {
		return nil, fmt.Errorf("no .raglit/ found from here — run this inside a project (raglit init)")
	}
	return raglit.LoadRelations(raglit.RelationsPath(dir))
}

func runMark(args []string) error {
	fs := flag.NewFlagSet("mark", flag.ExitOnError)
	supersedes := fs.String("supersedes", "", "for a version pair: the path that GOVERNS")
	note := fs.String("note", "", "why — the reasoning, for whoever reads this later")
	by := fs.String("by", "", "who ruled (default $RAGLIT_BY, else the OS user)")
	fs.Parse(args)

	if fs.NArg() != 3 {
		return fmt.Errorf("mark: want <A> <B> <copy|version|unrelated>")
	}
	a, b, kind := fs.Arg(0), fs.Arg(1), raglit.MarkKind(fs.Arg(2))

	rel, err := openRelations()
	if err != nil {
		return err
	}
	if prev, ok := rel.Get(a, b); ok {
		// Not an error: a correction is exactly what the append-only file is for.
		// But it is said out loud, because silently reversing an earlier ruling is
		// how a corpus ends up with two beliefs and no record of the change.
		fmt.Fprintf(os.Stderr, "note: this pair was already ruled %q%s — recording the new ruling over it\n",
			prev.Kind, byline(prev))
	}
	m := raglit.Mark{
		A: a, B: b, Kind: kind,
		Supersedes: *supersedes,
		Note:       *note,
		By:         who(*by),
		At:         time.Now().UTC().Format("2006-01-02"),
	}
	if err := rel.Add(m); err != nil {
		return fmt.Errorf("mark: %w", err)
	}
	fmt.Printf("%s: %s\n     %s\n", kind, m.A, m.B)
	if m.Supersedes != "" {
		fmt.Printf("  governed by %s\n", m.Supersedes)
	}
	fmt.Printf("  recorded in %s\n", rel.Path)
	return nil
}

func runMarks(args []string) error {
	fs := flag.NewFlagSet("marks", flag.ExitOnError)
	openStore, _ := addStoreFlags(fs)
	todo := fs.Bool("todo", false, "overlapping pairs nobody has ruled on yet, with a proposal for each")
	asJSON := fs.Bool("json", false, "emit as JSON")
	minCov := fs.Float64("min-coverage", 0, "only pairs reaching this block coverage")
	fs.Parse(args)

	rel, err := openRelations()
	if err != nil {
		return err
	}

	if !*todo {
		marks := rel.All()
		if fs.NArg() > 0 {
			marks = rel.For(fs.Arg(0))
		}
		if *asJSON {
			return emitJSON(marks)
		}
		if len(marks) == 0 {
			fmt.Println("no rulings recorded yet — 'raglit marks --todo' shows what is waiting")
			return nil
		}
		for _, m := range marks {
			printMark(m)
		}
		fmt.Printf("\n%d ruling(s) in %s\n", len(marks), rel.Path)
		return nil
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	opts := raglit.SimilarOpts{Width: raglit.FoldWidth, Mod: raglit.SampleMod}
	st, err := store.SketchStatusFor(opts.Width, opts.Mod)
	if err != nil {
		return err
	}
	if st.Sketched == 0 {
		return fmt.Errorf("marks --todo: nothing is sketched yet — run 'raglit similar --build' first")
	}
	if len(st.Unsketched) > 0 {
		// Said on stderr, never swallowed: an empty todo list from a half-sketched
		// corpus reads as "everything is ruled on", which is the opposite of true.
		fmt.Fprintf(os.Stderr,
			"warning: %d of %d documents are UNSKETCHED and were not compared — run 'raglit similar --build'\n",
			len(st.Unsketched), st.Documents)
	}

	_, pairs, err := collectPairs(store, opts)
	if err != nil {
		return err
	}

	type open struct {
		A, B     string          `json:"-"`
		Pair     [2]string       `json:"pair"`
		Proposal raglit.Proposal `json:"proposal"`
		Match    raglit.DocMatch `json:"match"`
	}
	var todos []open
	for _, pr := range pairs {
		if _, ruled := rel.Get(pr.A, pr.B); ruled {
			continue
		}
		if cov := maxf(pr.M.BlockCoverProbe, pr.M.BlockCoverMatch); cov < *minCov {
			continue
		}
		todos = append(todos, open{A: pr.A, B: pr.B, Pair: [2]string{pr.A, pr.B},
			Proposal: raglit.Propose(pr.M), Match: pr.M})
	}
	// Strongest overlap first: those are both the most likely to be real and the
	// cheapest to rule on.
	sort.SliceStable(todos, func(i, j int) bool {
		ki := maxf(todos[i].Match.BlockCoverProbe, todos[i].Match.BlockCoverMatch)
		kj := maxf(todos[j].Match.BlockCoverProbe, todos[j].Match.BlockCoverMatch)
		return ki > kj
	})

	if *asJSON {
		return emitJSON(todos)
	}
	if len(todos) == 0 {
		fmt.Printf("every overlapping pair has been ruled on (%d ruling(s) in %s)\n", len(rel.All()), rel.Path)
		return nil
	}
	fmt.Printf("%d pair(s) awaiting a ruling\n\n", len(todos))
	for _, t := range todos {
		proposed := string(t.Proposal.Kind)
		if proposed == "" {
			proposed = "open"
		} else if t.Proposal.Confident {
			proposed += " (clear)"
		}
		fmt.Printf("%-16s %s\n%-16s %s\n", t.Match.Relation, t.A, "", t.B)
		fmt.Printf("  coverage A %.3f  B %.3f\n", t.Match.BlockCoverProbe, t.Match.BlockCoverMatch)
		fmt.Printf("  proposed: %s — %s\n", proposed, t.Proposal.Why)
		fmt.Printf("  rule it:  raglit mark %q %q %s\n\n", t.A, t.B, firstWord(proposed))
	}
	return nil
}

func printMark(m raglit.Mark) {
	fmt.Printf("%-10s %s\n%-10s %s\n", m.Kind, m.A, "", m.B)
	if m.Supersedes != "" {
		fmt.Printf("  governs: %s\n", m.Supersedes)
	}
	if m.Note != "" {
		fmt.Printf("  %s\n", m.Note)
	}
	if s := byline(m); s != "" {
		fmt.Printf(" %s\n", strings.TrimPrefix(s, " "))
	}
	fmt.Println()
}

func byline(m raglit.Mark) string {
	switch {
	case m.By != "" && m.At != "":
		return fmt.Sprintf(" (%s, %s)", m.By, m.At)
	case m.At != "":
		return fmt.Sprintf(" (%s)", m.At)
	case m.By != "":
		return fmt.Sprintf(" (%s)", m.By)
	}
	return ""
}

func who(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("RAGLIT_BY"); v != "" {
		return v
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return ""
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
