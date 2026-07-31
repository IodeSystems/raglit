package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// Announcing the copies a purge leaves behind.
//
// A reread purges one document's cached pages and reads it again. That is the
// whole point when a read was wrong — but the corpus holds the same instrument
// more than once, routinely: a scan of a PDF already held, an exhibit reproduced
// inside a filing, the same letter arriving from a broker's file and again from
// an iCloud export.
//
// Those other holdings keep their OLD text. Nothing errors, nothing is stale by
// any check the index makes, and the corpus now disagrees with itself about one
// instrument — the re-read copy says one thing and its twin says what the bad
// read said. A citation that resolves to the twin gets the answer the reread was
// performed to remove.
//
// So the purge says what it did not touch. It does not touch them ITSELF: which
// copies should be re-read is a judgement (a version is a different instrument
// and may be correct as it stands), and silently rewriting documents a person
// did not name is not something a purge should do.

// announceOtherCopies reports documents holding the same content as path, and
// what to do about them. Best-effort throughout: a failure to look is never a
// reason to fail the reread that already happened.
func announceOtherCopies(store *raglit.Store, js *raglit.JudgementStore, path string) {
	type other struct {
		path string
		why  string
	}
	seen := map[string]other{}

	// Byte-identical holdings. The strongest statement available and independent
	// of any ruling: these are not similar to the purged document, they ARE it.
	if same, err := store.SameBytesAs(path); err == nil {
		for p := range same {
			seen[p] = other{p, "identical bytes"}
		}
	}

	// Ruled copies and versions. A copy is the same instrument and its cached
	// read is now the odd one out. A version is a DIFFERENT instrument that
	// happens to share most of its text, so it is reported with that word and
	// not lumped in — re-reading it may be exactly the wrong move.
	if js != nil {
		rels, _ := js.RelationsFor(path)
		for _, m := range rels {
			p, ok := m.Other(path)
			if !ok {
				continue
			}
			if _, already := seen[p]; already {
				continue
			}
			switch m.Kind {
			case raglit.MarkCopy:
				seen[p] = other{p, "ruled a copy"}
			case raglit.MarkVersion:
				w := "ruled a VERSION — a different filing, may be correct as it stands"
				if m.Supersedes == p {
					w = "ruled a VERSION, and the one that GOVERNS"
				} else if m.Supersedes == path {
					w = "ruled a VERSION, superseded by the document just purged"
				}
				seen[p] = other{p, w}
			}
		}
	}

	if len(seen) == 0 {
		return
	}
	others := make([]other, 0, len(seen))
	for _, o := range seen {
		others = append(others, o)
	}
	sort.Slice(others, func(i, j int) bool { return others[i].path < others[j].path })

	fmt.Fprintf(os.Stderr, "  ⚠ the index holds %d other cop%s of this content, still on the OLD read:\n",
		len(others), plural(len(others), "y", "ies"))
	for _, o := range others {
		fmt.Fprintf(os.Stderr, "      %s  (%s)\n", o.path, o.why)
	}
	ps := make([]string, len(others))
	for i, o := range others {
		ps[i] = o.path
	}
	fmt.Fprintf(os.Stderr, "    re-read the ones that should match: raglit reread %s\n", quoteArgs(ps))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// quoteArgs renders paths as a runnable argument list. Paths in this corpus
// carry spaces and colons, so an unquoted list is a command that fails.
func quoteArgs(paths []string) string {
	qs := make([]string, len(paths))
	for i, p := range paths {
		qs[i] = fmt.Sprintf("%q", p)
	}
	return strings.Join(qs, " ")
}
