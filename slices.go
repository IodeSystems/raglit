package raglit

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Slicing a bundle into the documents inside it.
//
// A scan is routinely several instruments in one file: a declaration and its
// exhibits, a disclosure packet, a recorder's batch. The file is what was filed,
// so it is the evidence and it does not get cut up — producing six files where
// one was served creates a provenance problem nobody wants to explain. But the
// instruments inside it are what a fact actually cites, and "page 9 of the
// 40-page scan" is not a document you can rule on, compare, or supersede.
//
// So a slice DECLARES that a page range of a bundle is a document, and raglit
// derives a citable child from it. Three properties make that safe:
//
//	The bundle is untouched. It stays the single source of bytes, so a bad read
//	is fixed in ONE place and every child re-derives — no second copy to drift.
//
//	The transcript is untouched. It is generated output, rewritten on every
//	read, and a slice recorded there would vanish at the next one. It also SHOULD
//	stay whole: a bundle's transcript is an accurate representation of the
//	bundle, which is a different and equally necessary thing from the documents
//	inside it.
//
//	Page numbers stay the PARENT's. A child of pages 3-6 has pages 3, 4, 5, 6 —
//	never renumbered to 1-4. Renumbering makes a quotation cited from the child
//	uncheckable against the exhibit as filed, which in a litigation corpus is the
//	whole game.
//
// The declaration is durable and lives in the judgement database beside the
// documents, for the same reason every ruling does: nothing in the bytes says
// pages 3-6 are one instrument, so it cannot be recomputed and must outlive
// every reindex. This file holds the shape; judgements.go holds the storage.

// Slice declares that a page range of Parent is a document in its own right.
type Slice struct {
	// ID is the child's stable identity. Facts cite it, so it must survive
	// re-reads and must NOT be derived from the page range: a bundle re-paginated
	// by a better read would otherwise rename every child that cites it.
	ID     string `json:"id"`
	Parent string `json:"parent"`
	// From and To are inclusive PARENT page numbers.
	From int `json:"from"`
	To   int `json:"to"`
	// Title is what this instrument is, in a person's words. The one field that
	// makes a slice worth more than the page range it wraps.
	Title string `json:"title,omitempty"`
	Note  string `json:"note,omitempty"`
	By    string `json:"by,omitempty"`
	At    string `json:"at,omitempty"`
}

// Pages returns the parent page numbers this slice covers.
func (s Slice) Pages() []int {
	if s.From <= 0 || s.To < s.From {
		return nil
	}
	out := make([]int, 0, s.To-s.From+1)
	for p := s.From; p <= s.To; p++ {
		out = append(out, p)
	}
	return out
}

// ChildPath is the derived document's path in the index: the parent's path with
// the range appended after '#'.
//
// A '#' suffix rather than a new file, because there IS no new file and the path
// should say so. It also keeps provenance in front of anyone reading a search
// result — a hit in "scan.pdf#p3-6" is visibly a hit inside scan.pdf, which a
// separate filename would hide.
func (s Slice) ChildPath() string {
	return fmt.Sprintf("%s#p%d-%d", s.Parent, s.From, s.To)
}

// IsChildPath reports whether a document path denotes a derived slice rather
// than a file. Anything that checks the filesystem must ask this first: a child
// has no file, and reading its absence as "the source vanished" would delete
// documents that are working exactly as designed.
func IsChildPath(p string) bool { return strings.Contains(p, "#p") && ChildParent(p) != "" }

// ChildParent returns the bundle a child path derives from, or "".
func ChildParent(p string) string {
	i := strings.LastIndex(p, "#p")
	if i < 0 {
		return ""
	}
	rng := p[i+2:]
	j := strings.IndexByte(rng, '-')
	if j <= 0 {
		return ""
	}
	if _, err := strconv.Atoi(rng[:j]); err != nil {
		return ""
	}
	if _, err := strconv.Atoi(rng[j+1:]); err != nil {
		return ""
	}
	return p[:i]
}

func (s Slice) validate() error {
	switch {
	case s.ID == "":
		return fmt.Errorf("a slice needs an id")
	case s.Parent == "":
		return fmt.Errorf("a slice needs a parent")
	case s.From <= 0:
		return fmt.Errorf("page numbers are the parent's and start at 1, got from=%d", s.From)
	case s.To < s.From:
		return fmt.Errorf("range runs backwards: %d-%d", s.From, s.To)
	}
	return nil
}

// Coverage reports which of a bundle's pages belong to no slice, and where
// slices overlap.
//
// This is what says a bundle is FULLY linearized rather than merely started.
// Without it "we sliced that scan" is unfalsifiable — pages 23-27 belonging to
// nothing looks identical to a finished job.
//
// Overlap is reported, not refused: a declaration and the exhibit bound into it
// genuinely occupy the same pages, and a tool that forbade that would force the
// person to lie about one of them.
type Coverage struct {
	Parent    string  `json:"parent"`
	Pages     int     `json:"pages"`
	Covered   int     `json:"covered"`
	Uncovered []int   `json:"uncovered,omitempty"`
	Overlaps  [][]int `json:"overlaps,omitempty"` // [page, count] for pages in >1 slice
}

// sliceCoverage computes coverage for one bundle over a known page count.
func sliceCoverage(parent string, pages int, slices []Slice) Coverage {
	c := Coverage{Parent: parent, Pages: pages}
	count := map[int]int{}
	for _, sl := range slices {
		for _, p := range sl.Pages() {
			count[p]++
		}
	}
	for p := 1; p <= pages; p++ {
		switch n := count[p]; {
		case n == 0:
			c.Uncovered = append(c.Uncovered, p)
		default:
			c.Covered++
			if n > 1 {
				c.Overlaps = append(c.Overlaps, []int{p, n})
			}
		}
	}
	return c
}

// ParseRange reads "3-6" or "3" as an inclusive page range.
//
// Inclusive because that is how a person reads a document: "pages 3-6" is four
// pages, and a half-open range here would silently drop the last page of every
// slice anybody declared by eye.
func ParseRange(s string) (from, to int, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "p")
	if i := strings.IndexAny(s, "-–"); i > 0 {
		a, e1 := strconv.Atoi(strings.TrimSpace(s[:i]))
		b, e2 := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s[i:], "-")))
		if e1 != nil || e2 != nil || a < 1 || b < a {
			return 0, 0, fmt.Errorf("bad page range %q — want N or N-M with 1 <= N <= M", s)
		}
		return a, b, nil
	}
	n, e := strconv.Atoi(s)
	if e != nil || n < 1 {
		// n < 1 catches "-4", which Atoi happily reads as negative four. Page
		// numbers are the parent's own and start at 1, so a non-positive one is
		// a typo, not a range.
		return 0, 0, fmt.Errorf("bad page range %q — want N or N-M, pages start at 1", s)
	}
	return n, n, nil
}

// MaterializeSlice writes the derived child document for a declaration.
//
// The child's text is the parent's pages, carried over unchanged, and its page
// numbers stay the PARENT's: a child of pages 3-6 has pages 3, 4, 5, 6. Never
// renumbered to 1-4 — a quotation cited from the child has to be checkable
// against the exhibit as filed, and renumbering makes "p. 1" mean two different
// sheets depending on which document you are holding.
//
// One fragment per page rather than the windowing fragmenter. The child exists
// to be cited and scoped, and the parent is already indexed at fragment grain
// for deep search; keeping page and fragment the same unit here means an offset
// in a child result needs no translation to become a page in the bundle.
//
// Idempotent: Ingest replaces a document's fragments, so re-running after the
// parent is re-read refreshes the child. That is the whole reason the bundle
// stays the single source of bytes — one thing to fix, and the children follow.
func MaterializeSlice(s *Store, sl Slice) (int, error) {
	if err := sl.validate(); err != nil {
		return 0, err
	}
	pages, err := s.TruePages(sl.Parent)
	if err != nil {
		return 0, fmt.Errorf("slice %s: %w", sl.ID, err)
	}
	var frags []Fragment
	var have []int
	for _, p := range pages {
		if p.Page < sl.From || p.Page > sl.To {
			continue
		}
		have = append(have, p.Page)
		if strings.TrimSpace(p.Text) == "" {
			// A blank page inside the range is still IN the child: page numbering
			// has to stay complete or a later alignment reports the wrong page.
			continue
		}
		frags = append(frags, Fragment{Page: p.Page, Ord: 0, Text: p.Text})
	}
	if len(have) == 0 {
		return 0, fmt.Errorf("slice %s: the parent has no pages in %d-%d (it has %d page(s))",
			sl.ID, sl.From, sl.To, len(pages))
	}
	title := sl.Title
	if title == "" {
		title = fmt.Sprintf("%s p%d-%d", filepath.Base(sl.Parent), sl.From, sl.To)
	}
	if err := s.Ingest(context.Background(), Document{
		Path: sl.ChildPath(), Title: title, Fragments: frags,
	}); err != nil {
		return 0, err
	}
	return len(frags), nil
}
