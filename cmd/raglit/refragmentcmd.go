package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/iodesystems/raglit"
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
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	client := addClientFlags(fs)
	dryRun := fs.Bool("dry-run", false, "list what would be re-ingested, change nothing")
	limitFlag := fs.Int("limit-chars", 0, "override the embed limit instead of probing")
	fs.Parse(args)

	store, where, err := openQueueStore(openStore, homeOf, client,
		fs.Lookup("db").Value.String() != "", fs.Lookup("index").Value.String())
	if err != nil {
		return err
	}
	defer store.Close()
	fmt.Printf("index: %s\n", where)

	lf.resolve(homeOf())
	cfg, _, _ := raglit.LoadConfig(homeOf())
	limit := *limitFlag
	if limit <= 0 {
		if *lf.embedModel == "" {
			return fmt.Errorf("no embed model configured — pass --limit-chars or run 'raglit init'")
		}
		emb := raglit.NewEmbedder(lf.embedClientForProbe(), *lf.embedModel)
		fmt.Fprintf(os.Stderr, "probing %s for its input limit...\n", *lf.embedModel)
		limit = store.EmbedLimitChars(context.Background(), emb, cfg.EmbedLimitChars)
	}
	if limit <= 0 {
		return fmt.Errorf("could not determine an embed limit — the endpoint may truncate silently; pass --limit-chars")
	}
	fmt.Printf("embed limit: %d chars\n", limit)

	over, err := store.OversizedDocs(limit)
	if err != nil {
		return err
	}
	if len(over) == 0 {
		fmt.Println("no document holds a fragment over the limit")
		return nil
	}
	paths := make([]string, 0, len(over))
	for p := range over {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return over[paths[i]] > over[paths[j]] })

	for _, p := range paths {
		fmt.Printf("  %7d chars  %s\n", over[p], p)
	}
	if *dryRun {
		fmt.Printf("\n%d document(s) would be re-ingested.\n", len(paths))
		return nil
	}
	n := 0
	for _, p := range paths {
		if err := store.MarkForReingest(p); err != nil {
			fmt.Fprintf(os.Stderr, "  failed %s: %v\n", p, err)
			continue
		}
		if _, err := store.Enqueue(p, ""); err != nil {
			fmt.Fprintf(os.Stderr, "  queue %s: %v\n", p, err)
			continue
		}
		n++
	}
	fmt.Printf("\n%d document(s) queued for re-fragmenting.\n", n)
	fmt.Println("Pages already transcribed come from the OCR cache, so this re-fragments without re-reading.")
	return nil
}
