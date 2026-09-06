package main

import (
	"flag"
	"fmt"
	"os"
)

// `raglit migrate-attachments` moves extracted mail attachments out of the
// corpus and into raglit's own storage.
//
// A command rather than something that happens on open. It renames files in
// somebody's document tree and rewrites document rows; that is not a thing to do
// to a person while they are asking for something else. It defaults to a dry run
// for the same reason.
func runMigrateAttachments(args []string) error {
	fs := flag.NewFlagSet("migrate-attachments", flag.ExitOnError)
	openStore, _ := addStoreFlags(fs)
	apply := fs.Bool("apply", false, "actually move them (default: report what would move)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: raglit migrate-attachments [--index NAME] [--apply]

Mail attachments used to be extracted into <archive>.raglit-attachments/ beside
the archive, inside the corpus. They now live in raglit's own storage. This moves
the ones already written, and rewrites their document rows so each keeps its
identity — its caption, history and notes — instead of becoming a missing file.

Reports what it would do unless --apply is given.
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	moves, err := store.MigrateExtractedAttachments(!*apply)
	if err != nil {
		return err
	}
	if len(moves) == 0 {
		fmt.Println("nothing to migrate — no extracted attachments are in the corpus")
		return nil
	}
	var failed int
	for _, m := range moves {
		switch {
		case m.Err != "":
			failed++
			fmt.Printf("  FAILED %s\n         %s\n", m.OldPath, m.Err)
		case *apply:
			fmt.Printf("  moved  %s\n      -> %s\n", m.OldPath, m.NewPath)
		default:
			fmt.Printf("  would move %s\n          -> %s\n", m.OldPath, m.NewPath)
		}
	}
	verb := "would move"
	if *apply {
		verb = "moved"
	}
	fmt.Printf("\n%s %d attachment document(s)", verb, len(moves)-failed)
	if failed > 0 {
		fmt.Printf(", %d FAILED", failed)
	}
	fmt.Println(".")
	if !*apply {
		fmt.Println("Nothing was changed. Re-run with --apply.")
	}
	return nil
}
