package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// `raglit quality-log export` — the one way a quality log leaves the machine.
//
// The log is local and full of real queries, paths and document ids. This
// command is the anonymize step: it replaces every identifier and piece of
// text with a hash that is stable for this export (so a session still joins
// its search to its fetch) and per-export (so no two exports share a value),
// and it keeps the quality signal — ranks, scores, origins, sizes, fetch/use
// flags. It prints what leaves before it leaves: the bytes shown are the
// bytes written, and with no --out they go to stdout and nowhere else.
func runQualityLog(args []string) error {
	fs := flag.NewFlagSet("quality-log", flag.ContinueOnError)
	export := fs.Bool("export", false, "anonymize the log and show what leaves")
	out := fs.String("out", "", "write the export to this file (the same bytes are shown first)")
	home := fs.String("home", "", "project home dir (default: nearest ./.raglit, else $RAGLIT_HOME)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*export {
		return fmt.Errorf("quality-log: nothing to do (the export is the only operation: `raglit quality-log --export` [--home DIR] [--out FILE])")
	}
	var h raglit.Home
	if *home != "" {
		h = raglit.Home(*home)
	} else {
		h = raglit.DiscoverHome()
	}

	key, err := raglit.NewAnonKey()
	if err != nil {
		return err
	}
	evs, err := raglit.AnonymizeExport(h, key)
	if err != nil {
		return err
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for i := range evs {
		if err := enc.Encode(evs[i]); err != nil {
			return err
		}
	}
	// What leaves: the count, the kinds, and the number of distinct values a
	// reader of the export would see. Shown before the bytes, and equal to
	// them.
	var nSearch, nFetch, nSurface int
	queries, paths, sessions, docs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, ev := range evs {
		switch ev.Type {
		case "search":
			nSearch++
			if ev.Query != "" {
				queries[ev.Query] = true
			}
		case "get_document":
			nFetch++
			if ev.Path != "" {
				paths[ev.Path] = true
			}
		case "surface":
			nSurface++
		}
		if ev.Session != "" {
			sessions[ev.Session] = true
		}
		for _, h2 := range ev.Hits {
			if h2.DocID != "" {
				docs[h2.DocID] = true
			}
		}
		if ev.DocID != "" {
			docs[ev.DocID] = true
		}
	}
	fmt.Printf("quality log: %d event(s) — %d search, %d get_document, %d surface\n",
		len(evs), nSearch, nFetch, nSurface)
	fmt.Printf("leaving: %d query, %d path, %d session, %d document — each a hash stable for THIS export only\n",
		len(queries), len(paths), len(sessions), len(docs))
	if *out != "" {
		fmt.Println("export goes to", *out, "— the lines below are the bytes it writes:")
	} else {
		fmt.Println("export goes to stdout and nowhere else — the lines below are it:")
	}
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteString(b.String()); err != nil {
			return err
		}
	}
	if len(evs) > 0 {
		fmt.Print(b.String())
	}
	return nil
}
