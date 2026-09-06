package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// Slicing a bundle into the documents inside it.
//
// A scan is routinely several instruments in one file. The file is what was
// filed, so it is not cut up — producing six files where one was served creates
// a provenance problem nobody wants to explain. But the instruments inside it
// are what a fact cites, and "page 9 of the 40-page scan" is not something you
// can rule on, compare, or supersede.
//
// So `slice` DECLARES that a page range is a document, and raglit derives a
// citable child from it. The bundle stays the single source of bytes, which is
// what makes a bad read fixable in one place: re-read the parent, re-materialize,
// and every child follows.

func runSlice(args []string) error {
	fs := flag.NewFlagSet("slice", flag.ExitOnError)
	id := fs.String("id", "", "stable identity for the child (default: derived from the title or range)")
	title := fs.String("title", "", "what this instrument IS, in your words")
	note := fs.String("note", "", "why — the reasoning, for whoever reads this later")
	by := fs.String("by", "", "who decided (default $RAGLIT_BY, else the OS user)")
	noBuild := fs.Bool("no-materialize", false, "declare only; do not build the child document now")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return fmt.Errorf("slice: want <DOC> <FROM-TO>  (e.g. raglit slice scan.pdf 3-6 --title \"record of survey\")")
	}
	parent, rng := pos[0], pos[1]
	from, to, err2 := raglit.ParseRange(rng)
	if err2 != nil {
		err = err2
		return fmt.Errorf("slice: %w", err)
	}

	// Resolve the parent against the DAEMON's index — the one search reads.
	// Opening a store locally here would consult the project-local index, which
	// on a daemon-routed project is a different and usually staler database, and
	// a child built into it is a child search can never find.
	pages, err := daemonPageCount(parent)
	if err != nil {
		return fmt.Errorf("slice: %w", err)
	}
	if from < 1 || to > pages {
		return fmt.Errorf("slice: %s has %d page(s); %d-%d is outside it", filepath.Base(parent), pages, from, to)
	}

	js, err := openJudgements()
	if err != nil {
		return err
	}
	defer js.Close()

	sl := raglit.Slice{
		ID: sliceID(*id, *title, parent, from, to), Parent: parent,
		From: from, To: to, Title: *title, Note: *note,
		By: who(*by), At: time.Now().UTC().Format("2006-01-02"),
	}
	if prev, ok, _ := js.Slice(sl.ID); ok && (prev.From != sl.From || prev.To != sl.To) {
		// Say it out loud. Silently moving a boundary changes what every fact
		// citing that child is resting on.
		fmt.Fprintf(os.Stderr, "note: %s already covered p%d-%d — recording the new range over it\n",
			sl.ID, prev.From, prev.To)
	}
	if err := js.PutSlice(sl); err != nil {
		return fmt.Errorf("slice: %w", err)
	}
	fmt.Printf("%s  p%d-%d of %s\n", sl.ID, sl.From, sl.To, filepath.Base(sl.Parent))
	if sl.Title != "" {
		fmt.Printf("  %s\n", sl.Title)
	}

	if !*noBuild {
		built, npages, err := daemonMaterialize(sl.ID)
		if err != nil {
			// Declared but not built is a recoverable state, not a lost ruling: the
			// declaration is in the audit trail and `slices --materialize` retries.
			fmt.Fprintf(os.Stderr, "  declared, but not built: %v\n", err)
			fmt.Fprintf(os.Stderr, "  retry with: raglit slices --materialize\n")
		} else if built > 0 {
			fmt.Printf("  built %s — %d page(s), citable as pages %d-%d of the bundle\n", sl.ChildPath(), npages, sl.From, sl.To)
		}
	}
	return reportCoverage(js, parent, pages)
}

// sliceID derives a stable identity. NOT from the page range: a bundle
// re-paginated by a better read would otherwise rename every child, and a fact
// citing one would dangle.
func sliceID(explicit, title, parent string, from, to int) string {
	if explicit != "" {
		return explicit
	}
	if title != "" {
		return slugify(title)
	}
	return fmt.Sprintf("%s-p%d-%d", slugify(strings.TrimSuffix(filepath.Base(parent), filepath.Ext(parent))), from, to)
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func runSlices(args []string) error {
	fs := flag.NewFlagSet("slices", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit as JSON")
	rebuild := fs.Bool("materialize", false, "rebuild every child document from its parent")
	propose := fs.Bool("propose", false, "propose slices from a bundle's own \"Page N of M\" counters")
	write := fs.Bool("write", false, "with --propose: record the proposals that are sliceable")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}

	if *propose {
		if len(pos) != 1 {
			return fmt.Errorf("slices --propose: name one document")
		}
		return runProposeSlices(fs, pos[0], *write, *asJSON)
	}

	js, err := openJudgements()
	if err != nil {
		return err
	}
	defer js.Close()

	var slices []raglit.Slice
	if len(pos) > 0 {
		slices, err = js.SlicesOf(pos[0])
	} else {
		slices, err = js.Slices()
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(slices)
	}
	if len(slices) == 0 {
		fmt.Println("no slices declared — raglit slice <DOC> <FROM-TO> --title \"...\"")
		return nil
	}

	if *rebuild {
		built, npages, err := daemonMaterialize("")
		if err != nil {
			return err
		}
		fmt.Printf("rebuilt %d child document(s), %d page(s)\n\n", built, npages)
	}

	var lastParent string
	for _, sl := range slices {
		if sl.Parent != lastParent {
			if lastParent != "" {
				fmt.Println()
			}
			fmt.Printf("%s\n", sl.Parent)
			lastParent = sl.Parent
		}
		fmt.Printf("  p%-9s %-28s %s\n", fmt.Sprintf("%d-%d", sl.From, sl.To), sl.ID, sl.Title)
	}

	// Coverage per bundle, which is what says a bundle is FULLY linearized
	// rather than merely started.
	parents, err := js.SliceParents()
	if err != nil {
		return err
	}
	fmt.Println()
	for _, p := range parents {
		if len(pos) > 0 && p != pos[0] {
			continue
		}
		pages, err := daemonPageCount(p)
		if err != nil {
			continue
		}
		if err := reportCoverage(js, p, pages); err != nil {
			return err
		}
	}
	return nil
}

// reportCoverage says what part of a bundle no slice claims.
//
// Without this "we sliced that scan" is unfalsifiable: pages 23-27 belonging to
// nothing looks exactly like a finished job. Overlap is reported and not
// refused — a declaration and the exhibit bound into it genuinely occupy the
// same pages, and a tool that forbade it would force someone to lie about one.
func reportCoverage(js *raglit.JudgementStore, parent string, pages int) error {
	c, err := js.CoverageOf(parent, pages)
	if err != nil {
		return err
	}
	fmt.Printf("coverage of %s: %d/%d page(s) claimed", filepath.Base(parent), c.Covered, c.Pages)
	if len(c.Uncovered) == 0 {
		fmt.Println(" — fully linearized")
	} else {
		fmt.Printf("\n  unclaimed: %s\n", compactPages(c.Uncovered))
	}
	if len(c.Overlaps) > 0 {
		ps := make([]int, 0, len(c.Overlaps))
		for _, o := range c.Overlaps {
			ps = append(ps, o[0])
		}
		fmt.Printf("  in more than one slice: %s\n", compactPages(ps))
	}
	return nil
}

// compactPages renders 1,2,3,7 as "1-3, 7" — a reader scanning for a gap wants
// the shape of it, not every number.
func compactPages(ps []int) string {
	if len(ps) == 0 {
		return "none"
	}
	var parts []string
	start, prev := ps[0], ps[0]
	flush := func() {
		if start == prev {
			parts = append(parts, fmt.Sprintf("%d", start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, prev))
		}
	}
	for _, p := range ps[1:] {
		if p == prev+1 {
			prev = p
			continue
		}
		flush()
		start, prev = p, p
	}
	flush()
	return strings.Join(parts, ", ")
}

// parseInterleaved parses flags that appear ANYWHERE in the argument list,
// returning the positional arguments in order.
//
// Go's flag package stops at the first non-flag token, so
// `raglit slice doc.pdf 3-6 --title "x"` silently drops --title and reports
// four positionals. People write flags last; a CLI that only accepts them first
// is one that fails in the shape everyone tries.
//
// Uses flag's own parsing repeatedly rather than guessing which flags take
// values — that knowledge lives in the FlagSet and duplicating it is how a
// hand-rolled splitter eats the value of a flag it does not know about.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// daemonIndexName is this project's namespaced index on the shared daemon.
//
// The daemon holds every project's indexes and namespaces them, so a request
// with no index name resolves to the bare "default" — a different, usually
// empty index. That failure is quiet in the worst way: the daemon answers
// successfully about the wrong database.
func daemonIndexName() (string, error) {
	ns, err := resolveProject("", raglit.DiscoverHome)
	if err != nil {
		return "", err
	}
	return nsIndex(ns, ""), nil
}

// daemonPageCount asks the daemon how many pages a document has.
func daemonPageCount(path string) (int, error) {
	base, err := ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return 0, err
	}
	idx, err := daemonIndexName()
	if err != nil {
		return 0, err
	}
	b, err := daemonGet(base, "/api/get-document", urlValues("path", path, "index", idx))
	if err != nil {
		return 0, err
	}
	var out struct {
		Pages []struct {
			Page int `json:"page"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	if len(out.Pages) == 0 {
		return 0, fmt.Errorf("the index holds no pages for %s — is it ingested?", filepath.Base(path))
	}
	max := 0
	for _, p := range out.Pages {
		if p.Page > max {
			max = p.Page
		}
	}
	return max, nil
}

// daemonMaterialize asks the daemon to build child documents. id == "" builds all.
func daemonMaterialize(id string) (built, pages int, err error) {
	dir, ok := raglit.ProjectDir()
	if !ok {
		return 0, 0, fmt.Errorf("no .raglit/ found from here")
	}
	base, err := ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return 0, 0, err
	}
	idx, err := daemonIndexName()
	if err != nil {
		return 0, 0, err
	}
	q := urlValues("project", dir, "index", idx)
	if id != "" {
		q.Set("id", id)
	}
	b, err := daemonPostJSON(base, "/api/slices/materialize?"+q.Encode(), map[string]any{})
	if err != nil {
		return 0, 0, err
	}
	var out struct {
		Built  int      `json:"built"`
		Pages  int      `json:"pages"`
		Failed []string `json:"failed"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, 0, err
	}
	for _, f := range out.Failed {
		fmt.Fprintf(os.Stderr, "  %s\n", f)
	}
	return out.Built, out.Pages, nil
}

func urlValues(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// daemonReread asks the daemon to purge a document's page cache and re-read it.
func daemonReread(path string) (purged int, jobID int64, err error) {
	base, err := ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return 0, 0, err
	}
	idx, err := daemonIndexName()
	if err != nil {
		return 0, 0, err
	}
	b, err := daemonPostJSON(base, "/api/reread?"+urlValues("path", path, "index", idx).Encode(), map[string]any{})
	if err != nil {
		return 0, 0, err
	}
	var out struct {
		Purged int   `json:"purged"`
		JobID  int64 `json:"job_id"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, 0, err
	}
	return out.Purged, out.JobID, nil
}

// daemonSketch asks the daemon to build page sketches in its own index.
func daemonSketch(rebuild bool, width, mod int) (n int, recipe string, skipped []string, err error) {
	base, err := ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return 0, "", nil, err
	}
	idx, err := daemonIndexName()
	if err != nil {
		return 0, "", nil, err
	}
	q := urlValues("index", idx)
	if rebuild {
		q.Set("rebuild", "true")
	}
	if width > 0 {
		q.Set("width", fmt.Sprintf("%d", width))
	}
	if mod > 0 {
		q.Set("mod", fmt.Sprintf("%d", mod))
	}
	b, err := daemonPostJSON(base, "/api/similar/build?"+q.Encode(), map[string]any{})
	if err != nil {
		return 0, "", nil, err
	}
	var out struct {
		Sketched int      `json:"sketched"`
		Recipe   string   `json:"recipe"`
		Skipped  []string `json:"skipped"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, "", nil, err
	}
	return out.Sketched, out.Recipe, out.Skipped, nil
}

// runProposeSlices reads a bundle's own page counters and proposes the
// instruments inside it. Proposes; does not record. A boundary is a claim about
// what a document IS, which is a ruling — and the counters lie often enough that
// a queue somebody skims is the right output.
func runProposeSlices(fs *flag.FlagSet, target string, write bool, asJSON bool) error {
	store, err := openCorpus(fs, func() (*raglit.Store, error) {
		return nil, fmt.Errorf("no local store")
	}, raglit.DiscoverHome)
	if err != nil {
		return err
	}
	defer store.Close()

	pages, err := store.TruePages(target)
	if err != nil {
		return fmt.Errorf("slices --propose: %w", err)
	}
	props := raglit.ProposeSlices(pages)
	if asJSON {
		return emitJSON(props)
	}
	if len(props) == 0 {
		fmt.Printf("no page counters found in %s — nothing to propose\n", filepath.Base(target))
		fmt.Println("  (a bundle without \"Page N of M\" markers has to be sliced by hand)")
		return nil
	}

	js, err := openJudgements()
	if err != nil {
		return err
	}
	defer js.Close()

	fmt.Printf("%s — %d page(s)\n\n", filepath.Base(target), len(pages))
	slices, seen := 0, 0
	for _, p := range props {
		if p.Sliceable() {
			slices++
			fmt.Printf("  p%-8s %-14s %s\n", fmt.Sprintf("%d-%d", p.From, p.To), p.Title, p.Why)
		} else {
			seen++
			fmt.Printf("  p%-8s %-14s %s\n", fmt.Sprintf("%d", p.From), p.Title, p.Why)
			fmt.Printf("  %-10s %-14s → NOT a slice: two instruments on one sheet cannot both be a\n", "", "")
			fmt.Printf("  %-10s %-14s   page range. Record each as `seen-in` this bundle instead.\n", "", "")
		}
	}
	fmt.Printf("\n%d sliceable, %d needing seen-in\n", slices, seen)

	if !write {
		if slices > 0 {
			fmt.Println("record the sliceable ones: raglit slices --propose --write <DOC>")
		}
		return nil
	}
	n := 0
	for _, p := range props {
		if !p.Sliceable() {
			continue
		}
		sl := raglit.Slice{
			ID:     sliceID("", p.Title+" "+filepath.Base(target), target, p.From, p.To),
			Parent: target, From: p.From, To: p.To,
			Title: p.Title,
			Note:  p.Why,
			By:    "raglit",
			At:    time.Now().UTC().Format("2006-01-02"),
		}
		if err := js.PutSlice(sl); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", sl.ID, err)
			continue
		}
		n++
	}
	fmt.Printf("recorded %d slice(s), attributed to raglit\n", n)
	if _, _, err := daemonMaterialize(""); err != nil {
		fmt.Fprintf(os.Stderr, "  declared but not built: %v\n", err)
	}
	return nil
}

// daemonCorrectPage asks the daemon to record a page correction.
//
// The daemon does it rather than the CLI because a correction changes the ACTIVE
// reading, and the row history of readings lives in the index — which the daemon
// owns as single writer.
func daemonCorrectPage(doc string, page int, note, by string, text []byte) (readings int, err error) {
	dir, ok := raglit.ProjectDir()
	if !ok {
		return 0, fmt.Errorf("no .raglit/ found from here")
	}
	base, err := ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return 0, err
	}
	idx, err := daemonIndexName()
	if err != nil {
		return 0, err
	}
	q := urlValues("project", dir, "index", idx, "doc", doc,
		"page", fmt.Sprintf("%d", page), "note", note, "by", by)
	resp, err := http.Post(strings.TrimRight(base, "/")+"/api/transcribe/correct?"+q.Encode(),
		"text/plain", bytes.NewReader(text))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("daemon correct: %s", strings.TrimSpace(string(b)))
	}
	var out struct {
		Readings int `json:"readings"`
	}
	_ = json.Unmarshal(b, &out)
	return out.Readings, nil
}

// daemonWithdraw asks the daemon to rule a document out of the corpus. Both
// halves — the trail event and the index rows — happen there, for the reason
// storeroute.go gives: the daemon is the single writer.
func daemonWithdraw(path, reason, by string) (refs []raglit.Reference, err error) {
	dir, ok := raglit.ProjectDir()
	if !ok {
		return nil, fmt.Errorf("no .raglit/ found from here")
	}
	base, err := ensureDaemon("", raglit.DiscoverHome)
	if err != nil {
		return nil, err
	}
	idx, err := daemonIndexName()
	if err != nil {
		return nil, err
	}
	q := urlValues("project", dir, "index", idx, "path", path, "reason", reason, "by", by)
	b, err := daemonPostJSON(base, "/api/withdraw?"+q.Encode(), map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		References []raglit.Reference `json:"references"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.References, nil
}
