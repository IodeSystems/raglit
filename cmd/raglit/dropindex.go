package main

import (
	"flag"
	"fmt"
	"net/url"

	"github.com/iodesystems/raglit"
)

// runDropIndex removes one index's storage — used by tools that create indexes
// per working tree and must not leave them behind.
//
// The POOL survives, deliberately. It holds the expensive half of ingest
// (extract/OCR/segment/embed, keyed by recipe+file and shared across every
// index), so an index deleted here is rebuildable from it without a single
// model call, and the same bytes ingested again anywhere reuse the cached work.
// Pool lifetime is governed by its own LRU GC, never by dropping an index.
func runDropIndex(args []string) error {
	fs := flag.NewFlagSet("drop-index", flag.ExitOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	client := addClientFlags(fs) // --daemon + --embedded + --project
	name := fs.String("index", "", "index to delete (required)")
	fs.Parse(args)

	if *name == "" && fs.NArg() == 1 {
		*name = fs.Arg(0)
	}
	if *name == "" {
		return fmt.Errorf("drop-index: need an index name (--index NAME)")
	}

	homeOf := func() raglit.Home {
		if *homeFlag != "" {
			return raglit.Home(*homeFlag)
		}
		return raglit.DiscoverHome()
	}
	dURL, ns, err := client(homeOf, false)
	if err != nil {
		return err
	}
	if dURL == "" {
		return fmt.Errorf("drop-index: indexes live in the daemon's scoped storage — remove --embedded/--db")
	}
	if _, err := daemonDelete(dURL, "/indexes", url.Values{"name": {nsIndex(ns, *name)}}); err != nil {
		return err
	}
	fmt.Printf("dropped index %q (the shared pool is untouched)\n", *name)
	return nil
}
