package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/iodesystems/raglit"
)

// runRegion puts back on the screen the exact image a passage was read from.
//
// The question this answers is not "what does this page say" — `regions` asked
// that. It is: a human has been asked to confirm that a document contains the
// words a fact quotes, and the machine did not read a whole page to produce
// them. It read a rotated, zoomed crop. Handing over the whole sheet hands over
// a DIFFERENT artifact, at the resolution where the survey's legal description
// vanished in the first place. So: same bbox, same rotation, same dpi, and a
// digest check that says whether it really is the same image.
func runRegion(args []string) error {
	fs := flag.NewFlagSet("region", flag.ExitOnError)
	list := fs.Bool("list", false, "list the recorded regions instead of rendering one")
	locate := fs.String("locate", "", "report which regions contain this text, deepest read first")
	out := fs.String("out", "-", "write the PNG here ('-' is stdout)")
	asSeen := fs.Bool("as-seen", false, "replay the context downscales — the image the model was given, not the crop")
	asJSON := fs.Bool("json", false, "--list/--locate as JSON")
	strict := fs.Bool("strict", false, "fail when the re-render does not reproduce the recorded digest")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: raglit region [--list|--locate TEXT] FILE [REGION-ID]")
	}
	path := fs.Arg(0)
	doc, ok, err := raglit.ReadRegionDoc(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no region read recorded for %s — run `raglit regions --write --page N %s` first",
			path, path)
	}

	switch {
	case *list:
		return printRegions(doc, allRegions(doc), *asJSON)
	case *locate != "":
		hits := locateRegions(doc, *locate)
		if len(hits) == 0 {
			// Not an error: "the recorded read does not contain these words" is a
			// finding, and a loud one — it is what a quotation nobody can source
			// looks like.
			fmt.Fprintln(os.Stderr, "no recorded region contains that text")
		}
		return printRegions(doc, hits, *asJSON)
	}

	if fs.NArg() != 2 {
		return fmt.Errorf("usage: raglit region [flags] FILE REGION-ID  (or --list)")
	}
	id := fs.Arg(1)
	rp, reg := doc.Find(id)
	if reg == nil {
		return fmt.Errorf("region %q is not in %s", id, raglit.RegionsPath(path))
	}

	dpi := reg.DPI
	if dpi == 0 {
		dpi = rp.DPI
	}
	page, err := renderPage(path, rp.Page, dpi)
	if err != nil {
		return err
	}
	// The rasterization is checked before the crop is taken. A page that came out
	// a different size at the same nominal dpi means a different renderer or a
	// re-exported document, and saying that is far more useful than reporting an
	// unexplained digest mismatch downstream.
	if b := page.Bounds(); rp.PxW > 0 && (b.Dx() != rp.PxW || b.Dy() != rp.PxH) {
		msg := fmt.Sprintf("page %d rasterized to %dx%d, but the read was taken from %dx%d at %d dpi",
			rp.Page, b.Dx(), b.Dy(), rp.PxW, rp.PxH, dpi)
		if *strict {
			return fmt.Errorf("raglit: %s", msg)
		}
		fmt.Fprintf(os.Stderr, "raglit: %s — the crop geometry still holds, the pixels do not\n", msg)
	}

	render := raglit.RerenderRegion
	if *asSeen {
		render = raglit.RerenderRegionAsSeen
	}
	img, err := render(page, reg)
	if err != nil {
		return err
	}

	// Verification is against the CROP, which is what the digest covers; the
	// as-seen image is derived from it deterministically and is a diagnostic.
	crop := img
	if *asSeen {
		if crop, err = raglit.RerenderRegion(page, reg); err != nil {
			return err
		}
	}
	if verr := raglit.VerifyRegionRender(reg, crop); verr != nil {
		if *strict {
			return verr
		}
		fmt.Fprintf(os.Stderr, "%v\n", verr)
	} else {
		fmt.Fprintf(os.Stderr, "region %s: reproduces the image its text was read from (sha %s)\n",
			reg.ID, reg.SHA256[:12])
	}
	describe(os.Stderr, rp, reg)

	if *out == "-" {
		// A PNG on a terminal is line noise, and the user meant to redirect it.
		if fi, serr := os.Stdout.Stat(); serr == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return fmt.Errorf("refusing to write a PNG to the terminal: redirect it, or pass --out FILE")
		}
		_, err = os.Stdout.Write(img)
		return err
	}
	if err := os.WriteFile(*out, img, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *out, len(img))
	return nil
}

// describe prints what a person about to look at the crop needs to know about
// how it was read — the flags especially, since a region still carrying
// low-resolution after descent is the honest "a human should look at this".
func describe(w *os.File, rp raglit.RegionPage, r *raglit.Region) {
	fmt.Fprintf(w, "  page %d  bbox %.1f,%.1f %.1fx%.1f%%  rot %d°  %d dpi  %.0f t/in²",
		rp.Page, r.BBox.X*100, r.BBox.Y*100, r.BBox.W*100, r.BBox.H*100,
		r.Rotation, r.DPI, r.TokensPerSqIn)
	if r.Downscales > 0 {
		fmt.Fprintf(w, "  (shrunk %dx to fit the model's context)", r.Downscales)
	}
	if len(r.Flags) > 0 {
		fmt.Fprintf(w, "  [%s]", strings.Join(r.Flags, " "))
	}
	fmt.Fprintln(w)
}

func allRegions(d *raglit.RegionDoc) []*raglit.Region {
	var out []*raglit.Region
	for _, p := range d.Pages {
		if p.Root != nil {
			out = append(out, p.Root.Flatten()...)
		}
	}
	return out
}

func locateRegions(d *raglit.RegionDoc, text string) []*raglit.Region {
	var out []*raglit.Region
	for _, p := range d.Pages {
		out = append(out, p.RegionsForQuote(text)...)
	}
	return out
}

func printRegions(d *raglit.RegionDoc, rs []*raglit.Region, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rs)
	}
	for _, r := range rs {
		fmt.Printf("%s\tp%d\t%s\t%.0fx%.0f%% @%d° %.0ft/in²",
			r.ID, r.Page, orRegion(r.Kind), r.BBox.W*100, r.BBox.H*100, r.Rotation, r.TokensPerSqIn)
		if len(r.Flags) > 0 {
			fmt.Printf("\t[%s]", strings.Join(r.Flags, " "))
		}
		if t := strings.TrimSpace(strings.SplitN(r.Text, "\n", 2)[0]); t != "" {
			fmt.Printf("\t%s", clip(t, 70))
		}
		fmt.Println()
	}
	return nil
}

func orRegion(k string) string {
	if k == "" {
		return "region"
	}
	return k
}
