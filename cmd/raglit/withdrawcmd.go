package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
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
	resolve := addTargetFlags(fs)
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
	t, err := resolve()
	if err != nil {
		return err
	}
	// Expand directories against what is INDEXED, not against the filesystem: the
	// question is which documents leave the corpus, and a file on disk that was
	// never indexed is not one of them.
	docs, err := t.documents()
	if err != nil {
		return err
	}
	paths := matchIndexed(docs, targets)
	if len(paths) == 0 {
		return fmt.Errorf("withdraw: nothing indexed under %s", strings.Join(targets, ", "))
	}
	// Both halves of a withdrawal — the trail event and the index rows — happen
	// in the daemon, the single writer.
	for _, p := range paths {
		fmt.Printf("%s\n", p)
		var refs []raglit.Reference
		if *dry {
			refs, err = t.references(p)
		} else {
			refs, err = daemonWithdraw(t, p, *reason, *by)
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
	resolve := addTargetFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := resolve()
	if err != nil {
		return err
	}
	var out struct {
		Withdrawals []raglit.Withdrawal `json:"withdrawals"`
	}
	if err := t.get("/api/withdrawals", nil, &out); err != nil {
		return err
	}
	ws := out.Withdrawals
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

func defaultWithdrawBy() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return ""
}
