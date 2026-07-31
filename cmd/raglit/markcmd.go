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

// openJudgements opens this project's judgement database, migrating a legacy
// relations.jsonl into it on first open.
//
// The migration is one-shot and idempotent: rulings upsert by pair, so running
// it twice records nothing new. The old file is left on disk rather than
// deleted — it is somebody's decisions, and a conversion that removes the only
// other copy of them is not a conversion anyone should trust.
func openJudgements() (*raglit.JudgementStore, error) {
	dir, ok := raglit.ProjectDir()
	if !ok {
		return nil, fmt.Errorf("no .raglit/ found from here — run this inside a project (raglit init)")
	}
	js, err := raglit.OpenJudgements(raglit.JudgementsPath(dir), raglit.AuditPath(dir))
	if err != nil {
		return nil, err
	}
	legacy, err := raglit.ReadLegacyRelations(raglit.LegacyRelationsPath(dir))
	if err != nil {
		js.Close()
		return nil, err
	}
	for _, m := range legacy {
		if _, ok, err := js.Relation(m.A, m.B); err == nil && !ok {
			if err := js.PutRelation(m); err != nil {
				js.Close()
				return nil, fmt.Errorf("migrating relations.jsonl: %w", err)
			}
		}
	}
	return js, nil
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

	js, err := openJudgements()
	if err != nil {
		return err
	}
	defer js.Close()
	if prev, ok, _ := js.Relation(a, b); ok {
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
	if err := js.PutRelation(m); err != nil {
		return fmt.Errorf("mark: %w", err)
	}
	fmt.Printf("%s: %s\n     %s\n", kind, m.A, m.B)
	if m.Supersedes != "" {
		fmt.Printf("  governed by %s\n", m.Supersedes)
	}
	fmt.Printf("  recorded in the judgement database\n")
	return nil
}

func runMarks(args []string) error {
	fs := flag.NewFlagSet("marks", flag.ExitOnError)
	openStore, _ := addStoreFlags(fs)
	todo := fs.Bool("todo", false, "overlapping pairs nobody has ruled on yet, with a proposal for each")
	identical := fs.Bool("identical", false, "documents held more than once, BYTE for byte — needs no sketches")
	rebuild := fs.Bool("rebuild", false, "drop the projected database and replay the audit trail into it")
	write := fs.Bool("write", false, "with --identical: record them as copies (attributed to raglit, not to you)")
	asJSON := fs.Bool("json", false, "emit as JSON")
	minCov := fs.Float64("min-coverage", 0, "only pairs reaching this block coverage")
	fs.Parse(args)

	js, err := openJudgements()
	if err != nil {
		return err
	}
	defer js.Close()

	if *rebuild {
		n, err := js.Rebuild()
		if err != nil {
			return err
		}
		rels, _ := js.Relations()
		sls, _ := js.Slices()
		fmt.Printf("replayed %d event(s) → %d relation(s), %d slice(s)\n", n, len(rels), len(sls))
		return nil
	}

	if *identical {
		store, err := openStore()
		if err != nil {
			return err
		}
		defer store.Close()
		return runIdentical(store, js, *write, *asJSON)
	}

	if !*todo {
		marks, err := js.Relations()
		if err != nil {
			return err
		}
		if fs.NArg() > 0 {
			if marks, err = js.RelationsFor(fs.Arg(0)); err != nil {
				return err
			}
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
		fmt.Printf("\n%d ruling(s)\n", len(marks))
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
		if _, ruled, _ := js.Relation(pr.A, pr.B); ruled {
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
		all, _ := js.Relations()
		fmt.Printf("every overlapping pair has been ruled on (%d ruling(s))\n", len(all))
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

// runIdentical handles the one relation that needs no human.
//
// Everywhere else in this tool the rule is propose-then-rule, because a
// similarity number cannot decide what a pair MEANS: a scan of a deed and a
// re-recorded deed both align at 0.97, and only a person can say which. Byte
// identity is not that. Two files with one sha256 are the same document — there
// is no inference in it, nothing for a reader to weigh, and no reading of the
// evidence under which they disagree. Asking someone to confirm it is asking
// them to rubber-stamp arithmetic, and a queue full of rubber stamps is how the
// pairs that DO need judgement get skimmed past.
//
// So these are recorded automatically, and attributed to raglit rather than to
// the person running the command. That distinction is the point in a corpus that
// may be read by an opponent: `by: raglit` says a machine observed identical
// bytes, and `by: Carl Taylor` says a person formed a view. Collapsing the two
// would put a human's name on findings they never looked at.
//
// A person can still overrule it — relations.jsonl is append-only and the later
// line wins — which is what makes the automatic write safe rather than final.
func runIdentical(store *raglit.Store, js *raglit.JudgementStore, write, asJSON bool) error {
	groups, err := store.IdenticalGroups()
	if err != nil {
		return err
	}

	type openGroup struct {
		Paths []string `json:"paths"`
		New   int      `json:"unruled_pairs"`
	}
	var out []openGroup
	total, already := 0, 0
	for _, g := range groups {
		n := 0
		for i := 0; i < len(g); i++ {
			for j := i + 1; j < len(g); j++ {
				total++
				if _, ruled, _ := js.Relation(g[i], g[j]); ruled {
					already++
					continue
				}
				n++
			}
		}
		if n > 0 {
			out = append(out, openGroup{Paths: g, New: n})
		}
	}

	if !write {
		if asJSON {
			return emitJSON(out)
		}
		if len(out) == 0 {
			fmt.Printf("no unruled byte-identical documents (%d pair(s) already recorded)\n", already)
			return nil
		}
		for _, g := range out {
			fmt.Printf("identical bytes — %d file(s):\n", len(g.Paths))
			for _, p := range g.Paths {
				fmt.Printf("    %s\n", p)
			}
			fmt.Println()
		}
		fmt.Printf("%d group(s), %d unruled pair(s). Record them: raglit marks --identical --write\n",
			len(out), total-already)
		return nil
	}

	wrote := 0
	for _, g := range out {
		for i := 0; i < len(g.Paths); i++ {
			for j := i + 1; j < len(g.Paths); j++ {
				a, b := g.Paths[i], g.Paths[j]
				if _, ruled, _ := js.Relation(a, b); ruled {
					continue
				}
				if err := js.PutRelation(raglit.Mark{
					A: a, B: b, Kind: raglit.MarkCopy,
					Note:     "identical source bytes (sha256)",
					By:       "raglit",
					At:       time.Now().UTC().Format("2006-01-02"),
					Relation: "identical",
				}); err != nil {
					return fmt.Errorf("record %s: %w", a, err)
				}
				wrote++
			}
		}
	}
	if wrote == 0 {
		fmt.Printf("nothing to record (%d pair(s) already ruled)\n", already)
		return nil
	}
	fmt.Printf("recorded %d pair(s) as copies\n", wrote)
	fmt.Println("  attributed to raglit — a person's ruling on any of them still overrides it")
	return nil
}
