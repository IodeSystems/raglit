package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// `raglit withdraw` rules a document out of the corpus, with grounds.
//
// Separate from any delete, and deliberately harder to use: it demands a
// --reason and refuses without one. What it is for is the case where a document
// is genuinely present, genuinely readable, and genuinely does not belong — and
// where a later reader finding it absent deserves to know why rather than
// re-adding it.
//
// The trail is written first and the index second, the order audit.go explains.
// Both halves matter: the trail is what git blames and what the other machines
// sync, the index is what search and the ingest path consult.
func runWithdraw(args []string) error {
	fs := flag.NewFlagSet("withdraw", flag.ContinueOnError)
	reason := fs.String("reason", "", "grounds for the withdrawal (required)")
	by := fs.String("by", defaultWithdrawBy(), "who decided")
	dry := fs.Bool("dry-run", false, "show what would be withdrawn and what cites it")
	openStore, homeOf := addStoreFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()
	if len(targets) == 0 {
		return fmt.Errorf("withdraw: name a document or directory")
	}
	if strings.TrimSpace(*reason) == "" && !*dry {
		// The reason IS the withdrawal. Refusing here rather than defaulting is
		// the point: a stock reason nobody wrote is worse than none, because it
		// reads like a decision.
		return fmt.Errorf("withdraw: --reason is required — a withdrawal without grounds is a delete")
	}

	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()

	// Expand directories against what is INDEXED, not against the filesystem: the
	// question is which documents leave the corpus, and a file on disk that was
	// never indexed is not one of them.
	docs, err := st.Documents()
	if err != nil {
		return err
	}
	paths := matchIndexed(docs, targets)
	if len(paths) == 0 {
		return fmt.Errorf("withdraw: nothing indexed under %s", strings.Join(targets, ", "))
	}

	// A withdrawal WRITES — an event to the trail and rows out of the index — so
	// on a daemon-routed project both halves go through the daemon. Doing them
	// locally would be the second writer, and on this layout it would write a
	// different index from the one search reads.
	ns, _ := resolveProject("", homeOf)
	routed := ns != "" && !explicitStoreFlag(fs)

	ctx := context.Background()
	for _, p := range paths {
		fmt.Printf("%s\n", p)
		if *dry {
			// References read locally: a dry run must not write, and reading the
			// corpus read-only is exactly what openCorpus is for.
			refs, _ := st.ReferencesTo(ctx, p)
			printReferences(refs)
			continue
		}
		var refs []raglit.Reference
		if routed {
			refs, err = daemonWithdraw(p, *reason, *by)
		} else {
			refs, err = withdrawLocally(ctx, st, p, *reason, *by)
		}
		if err != nil {
			return err
		}
		printReferences(refs)
	}
	verb := "withdrew"
	if *dry {
		verb = "would withdraw"
	}
	fmt.Printf("%s %d document(s)\n", verb, len(paths))
	return nil
}

// matchIndexed resolves each target to the indexed documents it names: an exact
// path, or every document beneath it when the target is a directory prefix.
func matchIndexed(docs []raglit.DocSummary, targets []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range targets {
		abs, err := filepath.Abs(t)
		if err != nil {
			abs = t
		}
		prefix := strings.TrimSuffix(abs, "/") + "/"
		for _, d := range docs {
			if seen[d.Path] {
				continue
			}
			if d.Path == abs || d.Path == t || strings.HasPrefix(d.Path, prefix) {
				out = append(out, d.Path)
				seen[d.Path] = true
			}
		}
	}
	return out
}

// runWithdrawn lists what has been ruled out and why.
func runWithdrawn(args []string) error {
	fs := flag.NewFlagSet("withdrawn", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable")
	openStore, homeOf := addStoreFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	ws, err := st.Withdrawals()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(ws)
	}
	if len(ws) == 0 {
		fmt.Println("nothing withdrawn")
		return nil
	}
	for _, w := range ws {
		fmt.Printf("%s\n    %s", w.Path, w.Reason)
		if w.By != "" {
			fmt.Printf(" — %s", w.By)
		}
		if w.At != "" {
			fmt.Printf(", %s", w.At)
		}
		fmt.Println()
	}
	fmt.Printf("%d document(s) withdrawn\n", len(ws))
	return nil
}

// printReferences names what still cites a withdrawn document.
//
// Reported, never rewritten. A reference is a claim its author made in another
// document, and editing it here would change that document's meaning without
// them knowing — so the withdrawal names them and the author decides.
func printReferences(refs []raglit.Reference) {
	for _, r := range refs {
		fmt.Printf("    ⚠ cited by %s: %s\n", r.From, clip(r.Excerpt, 120))
	}
}

// withdrawLocally is the embedded path: no daemon, so this process is the only
// writer and may do both halves itself.
func withdrawLocally(ctx context.Context, st *raglit.Store, path, reason, by string) ([]raglit.Reference, error) {
	js, err := openJudgements()
	if err != nil {
		return nil, fmt.Errorf("the trail is where the grounds live: %w", err)
	}
	defer js.Close()
	refs, _ := st.ReferencesTo(ctx, path)
	w := raglit.Withdrawal{Path: path, Reason: reason, By: by,
		At: time.Now().UTC().Format("2006-01-02")}
	if err := js.Withdraw(w); err != nil {
		return refs, err
	}
	return refs, st.Withdraw(w)
}

func defaultWithdrawBy() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return ""
}
