package main

import (
	"fmt"
	"os"

	flag "github.com/spf13/pflag"
)

// runRefragment re-ingests documents whose fragments are larger than the embed
// model will accept.
//
// Needed because the limit is discovered, not configured: until it is known,
// fragments are sized by taste, and everything already indexed was cut to that
// standard. Probing without correcting the back catalogue leaves a corpus whose
// old documents fail to embed and whose new ones do not, which is worse than
// either alone because the difference is invisible.
func runRefragment(args []string) error {
	fs := flag.NewFlagSet("refragment", flag.ExitOnError)
	resolve := addTargetFlags(fs)
	dryRun := fs.Bool("dry-run", false, "list what would be re-ingested, change nothing")
	limitFlag := fs.Int("limit-chars", 0, "override the embed limit instead of probing")
	fs.Parse(args)
	t, err := resolve()
	if err != nil {
		return err
	}
	fmt.Printf("index: %s\n", t.local())
	if *limitFlag <= 0 {
		fmt.Fprintln(os.Stderr, "probing the daemon's embed model for its input limit...")
	}
	var out struct {
		LimitChars int `json:"limit_chars"`
		Documents  []struct {
			Path  string `json:"path"`
			Chars int    `json:"chars"`
		} `json:"documents"`
		Queued int      `json:"queued"`
		Failed []string `json:"failed"`
	}
	if err := t.post("/api/refragment", map[string]any{"limit_chars": *limitFlag, "dry_run": *dryRun}, &out); err != nil {
		return err
	}
	fmt.Printf("embed limit: %d chars\n", out.LimitChars)
	if len(out.Documents) == 0 {
		fmt.Println("no document holds a fragment over the limit")
		return nil
	}
	for _, d := range out.Documents {
		fmt.Printf("  %7d chars  %s\n", d.Chars, d.Path)
	}
	if *dryRun {
		fmt.Printf("\n%d document(s) would be re-ingested.\n", len(out.Documents))
		return nil
	}
	for _, f := range out.Failed {
		fmt.Fprintf(os.Stderr, "  failed %s\n", f)
	}
	fmt.Printf("\n%d document(s) queued for re-fragmenting.\n", out.Queued)
	fmt.Println("Pages already transcribed come from the OCR cache, so this re-fragments without re-reading.")
	return nil
}
