package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// Backfilling what a region's PIXELS were measured to be, onto reads taken
// before anything measured them.
//
// This costs no model calls. Every region records the crop it was read from —
// bbox, rotation, dpi, and a digest of the bytes — so the image can be
// reproduced exactly and measured now. What comes out is not a new reading; it
// is a statement about the image the old reading was taken from.
//
// Which is the useful thing to know before spending tokens: 151 transcribed
// documents is too many to re-read on a hunch, and `blurred` or `faded` on a
// region says which pages a re-read would actually be for.

type damageTally struct {
	regions   int
	flagged   int
	unchanged int
	// mismatched regions are the ones that did NOT re-render to their recorded
	// digest. Counted and REFUSED rather than measured: a flag written from a
	// different image than the one that was read is a lie about provenance, and
	// this whole record exists so that provenance means something.
	mismatched int
	notes      []string
}

// runBackfillDamage measures every recorded region of every named document and
// records `blurred` / `faded` where the pixels say so.
func runBackfillDamage(paths []string, apply bool, quiet bool) error {
	if len(paths) == 0 {
		return fmt.Errorf("usage: raglit regions --backfill-damage [--apply] FILE...")
	}
	var total damageTally
	worth := map[string][]string{}

	for _, path := range paths {
		t, found, hits, err := backfillOne(path, apply)
		if err != nil {
			return err
		}
		if !found {
			if !quiet {
				fmt.Fprintf(os.Stderr, "%s: no region record; nothing to backfill\n", filepath.Base(path))
			}
			continue
		}
		if len(hits) > 0 {
			worth[path] = hits
		}
		if !quiet {
			fmt.Printf("%-52s %3d regions  %3d newly flagged  %3d clean  %3d unreproducible\n",
				trimName(filepath.Base(path), 52), t.regions, t.flagged, t.unchanged, t.mismatched)
			for _, n := range t.notes {
				fmt.Printf("    ! %s\n", n)
			}
		}
		total.regions += t.regions
		total.flagged += t.flagged
		total.unchanged += t.unchanged
		total.mismatched += t.mismatched
	}
	report(total, worth, apply)
	return nil
}

// backfillOne measures every recorded region of one document. Split out from the
// reporting so the measurement can be tested without parsing stdout.
func backfillOne(path string, apply bool) (damageTally, bool, []string, error) {
	var t damageTally
	var hits []string
	doc, ok, err := raglit.ReadRegionDoc(path)
	if err != nil {
		return t, false, nil, err
	}
	if !ok {
		return t, false, nil, nil
	}
	{
		var changedPages []raglit.RegionPage
		for _, page := range doc.Pages {
			if page.Root == nil {
				continue
			}
			img, err := renderPage(path, page.Page, page.DPI)
			if err != nil {
				t.notes = append(t.notes, fmt.Sprintf("page %d: %v", page.Page, err))
				continue
			}
			changed := false
			for _, reg := range page.Root.Flatten() {
				t.regions++
				if reg.SHA256 == "" {
					continue // never read; nothing was taken from an image
				}
				crop, err := raglit.RerenderRegion(img, reg)
				if err != nil {
					t.notes = append(t.notes, fmt.Sprintf("%s: %v", reg.ID, err))
					continue
				}
				if err := raglit.VerifyRegionRender(reg, crop); err != nil {
					// The page rasterizes differently now than it did then. Measuring
					// this image would describe something the old text did not come
					// from.
					t.mismatched++
					continue
				}
				before := len(reg.Flags)
				for _, f := range raglit.DamageOfPNG(crop) {
					reg.AddFlag(f)
				}
				if len(reg.Flags) > before {
					t.flagged++
					changed = true
					hits = append(hits, fmt.Sprintf("%s %v", reg.ID, reg.Flags))
				} else {
					t.unchanged++
				}
			}
			if changed {
				changedPages = append(changedPages, page)
			}
		}
		if apply && len(changedPages) > 0 {
			if _, werr := raglit.WriteRegionDoc(path, changedPages...); werr != nil {
				return t, true, hits, werr
			}
		}
	}
	return t, true, hits, nil
}

func report(total damageTally, worth map[string][]string, apply bool) {
	fmt.Printf("\n%d regions measured: %d flagged, %d clean, %d could not be reproduced\n",
		total.regions, total.flagged, total.unchanged, total.mismatched)
	if len(worth) > 0 {
		fmt.Printf("\nWorth a re-read — the pixels these were transcribed from measure as damaged:\n")
		names := make([]string, 0, len(worth))
		for k := range worth {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("  %s\n", filepath.Base(n))
			for _, r := range worth[n] {
				fmt.Printf("      %s\n", r)
			}
		}
	}
	if !apply {
		fmt.Printf("\nNothing written. Re-run with --apply to record these flags.\n")
	}
}

func trimName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// collectRegionDocs expands directories to the documents under them that have a
// region record, so a corpus can be swept without naming every file.
func collectRegionDocs(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			out = append(out, a)
			continue
		}
		err = filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !raglit.IsRegionsSidecar(p) {
				return err
			}
			out = append(out, strings.TrimSuffix(p, raglit.RegionsSuffix()))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
